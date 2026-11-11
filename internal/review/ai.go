package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"lux/internal/domain"
)

// ai.go sends anonymized timeline facts to the user's configured compatible model.
// Requests contain champion slots, sampled statistics, and observed events rather
// than player names, persistent identifiers, client credentials, or replay files.
// Coaching must distinguish observations from conditional advice and cite supplied
// phase or event identifiers before the result can enter the interface.
//
// A bounded response decoder rejects malformed or uncited analysis instead of
// falling back to heuristics labeled as AI. Redirects cannot carry provider keys
// to another origin. Context cancellation stops network work, and provider errors
// reveal only status classifications, excluding returned bodies and request secrets.

// modelHTTP bounds provider latency and rejects authenticated redirect destinations for every compatible service request.
var modelHTTP = &http.Client{Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// coachInstructions defines the evidence and output contract shared by compatible models.
const coachInstructions = `你是英雄联盟复盘教练。仅依据用户提供的真实时间线事实，用简体中文给出可操作复盘。名字和账号已脱敏。
数据限制：位置是分钟级离散采样，不是连续轨迹；未提供兵线状态、视野覆盖、技能释放、召唤师技能冷却、沟通或操作录像。资源事件的助攻字段可能不完整，不能从未被列入推断某人缺席。不知道就明确说无法判断。经济领先扩大不自动代表决策正确，落后也不自动代表失误；辅助与大乱斗尤其不能直接套用补刀指标。只比较用户选定的本人和对比选手，不把他人事件算到本人。
结合阶段经济/经验/补刀变化、击杀死亡和资源事件，指出打得好的时间段、值得回看的时间段，以及同阶段对方有证据支持的行为。对“应该怎么做”的建议标为条件性建议，避免从结果倒推唯一正确决策。尽可能给出亮点与改进项；证据不足时可给 neutral 并解释。只选择最有价值的 3–8 个阶段；没有足够证据时允许更少。
只返回 JSON，无 Markdown。结构：{"summary":"简短总评","insights":[{"windowId":"W1","verdict":"good 或 review 或 neutral","title":"简短发现","reason":"事实与解释","opponent":"本阶段对方可确认的行为及无法确认的部分","advice":"下次可执行的建议，说明前提","evidenceIds":["W1","E1"]}],"goals":["下一局练习目标"]}。
每条 finding 必须引用其 windowId 及至少一个本阶段有效 evidenceId（可以是 W1 本身）。不要编造其他时间、人物、事件、伤害值、胜率或评分。summary 不超过 160 字，每条 reason/opponent/advice 不超过 180 字，goals 最多 3 条。`

// modelFacts removes display identities while preserving selected-player evidence and source limits.
// Only the viewed and selected opposing slots contribute sampled measurements.
// The anonymous roster retains champion names, teams, and explicit roles so event
// actors remain interpretable without sending their account names or persistent IDs.
// Phase boundaries, measurement gaps, event references, and actual sampling intervals
// survive the conversion, allowing coaching to cite observed facts while acknowledging
// the missing continuous movement, vision, and ability information.
func modelFacts(view domain.TimelineReview) any {
	players := []map[string]any{}
	roster := []map[string]any{}
	for _, p := range view.Timeline.Players {
		member := map[string]any{"id": p.ID, "championId": p.Champion, "championName": p.ChampionName, "role": p.Role, "team": p.Team}
		roster = append(roster, member)
		if p.ID == view.Timeline.OwnID || p.ID == view.Opponent {
			players = append(players, member)
		}
	}
	samples := []domain.TimelineFrame{}
	for _, frame := range view.Timeline.Frames {
		samples = append(samples, domain.TimelineFrame{At: frame.At, Players: []domain.TimelineStats{Stats(frame, view.Timeline.OwnID), Stats(frame, view.Opponent)}})
	}
	return map[string]any{"mapId": view.Timeline.MapID, "queueId": view.Timeline.QueueID, "version": view.Timeline.Version, "timestampUnit": "milliseconds", "sampleSeconds": view.Timeline.SampleSeconds, "self": view.Timeline.OwnID, "opponent": view.Opponent, "players": players, "roster": roster, "samples": samples, "windows": view.Windows}
}

// validateAnalysis accepts only bounded findings whose citations belong to their actual phase.
// Existing phase identifiers may cite their aggregate facts. Event references must
// occur inside that same phase, preventing valid-looking identifiers from supporting
// a claim about a different interval or another game's unrelated event sequence.
// This verifies traceability, not the truth of the model's tactical interpretation;
// the interface exposes those references for the user's own review of each claim.
func validateAnalysis(result domain.AIReview, view domain.TimelineReview) error {
	if strings.TrimSpace(result.Summary) == "" || utf8.RuneCountInString(result.Summary) > 600 || len(result.Insights) == 0 || len(result.Insights) > 12 || len(result.Goals) > 5 {
		return errors.New("AI 返回的复盘结构不完整，请重试")
	}
	for _, finding := range result.Insights {
		var phase *domain.ReviewWindow
		for i := range view.Windows {
			if view.Windows[i].ID == finding.WindowID {
				phase = &view.Windows[i]
				break
			}
		}
		if phase == nil || len(finding.EvidenceIDs) == 0 || len(finding.EvidenceIDs) > 8 {
			return errors.New("AI 引用了不存在的时间段或缺少依据，请重试")
		}
		if finding.Verdict != "good" && finding.Verdict != "review" && finding.Verdict != "neutral" {
			return errors.New("AI 返回的阶段判断无法识别，请重试")
		}
		for _, value := range []string{finding.Title, finding.Reason, finding.Opponent, finding.Advice} {
			if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 800 {
				return errors.New("AI 返回的复盘内容不完整或过长，请重试")
			}
		}
		for _, id := range finding.EvidenceIDs {
			valid := id == phase.ID
			for _, event := range phase.Events {
				valid = valid || event.ID == id
			}
			if !valid {
				return errors.New("AI 引用与对应时间段不符，请重试")
			}
		}
	}
	for _, goal := range result.Goals {
		if strings.TrimSpace(goal) == "" || utf8.RuneCountInString(goal) > 300 {
			return errors.New("AI 返回的练习目标无效，请重试")
		}
	}
	return nil
}

// Analyze requests coaching from the configured provider and validates every finding citation.
// The caller supplies a verified opposing comparison and owns cancellation. Provider
// responses are not persisted or inserted into shared match snapshots. Unsupported
// structured output is handled by an ordinary JSON prompt, retaining compatibility
// with services that implement only the standard chat-completions message contract.
// A configuration copy keeps the selected model paired with its own credential
// while an independent settings save replaces preferences for subsequent requests.
func (s *AIService) Analyze(ctx context.Context, view domain.TimelineReview) (domain.AIReview, error) {
	var result domain.AIReview
	s.mu.RLock()
	config, key := s.config, s.key
	s.mu.RUnlock()
	base, err := normalizeBase(config.BaseURL)
	if err != nil || config.Model == "" {
		return result, errors.New("请先在 AI 设置中保存接口地址和模型名称")
	}
	if view.Opponent == 0 {
		return result, errors.New("请先选择本局对比选手")
	}
	facts, _ := json.Marshal(modelFacts(view))
	// A one-megabyte fact budget avoids unexpectedly sending oversized game telemetry; ordinary minute-sampled matches remain well below this bound.
	if len(facts) > 1<<20 {
		return result, errors.New("本局时间线过长，暂不支持整局 AI 分析")
	}
	body, _ := json.Marshal(map[string]any{"model": config.Model, "stream": false, "messages": []map[string]string{{"role": "system", "content": coachInstructions}, {"role": "user", "content": string(facts)}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return result, errors.New("AI 请求无法创建")
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := modelHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result, errors.New("AI 分析已取消或超时")
		}
		return result, errors.New("无法连接 AI 服务，请检查地址和网络")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		switch response.StatusCode {
		case 401, 403:
			return result, errors.New("AI 服务鉴权失败，请检查 API Key")
		case 429:
			return result, errors.New("AI 服务限流或额度不足，请稍后重试")
		default:
			return result, fmt.Errorf("AI 服务返回 HTTP %d，请检查接口地址和模型", response.StatusCode)
		}
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&reply) != nil || len(reply.Choices) == 0 {
		return result, errors.New("AI 服务返回格式不可用")
	}
	content := strings.TrimSpace(reply.Choices[0].Message.Content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(content, "```json"), "```"), "```"))
	}
	if json.Unmarshal([]byte(content), &result) != nil {
		return result, errors.New("AI 没有返回可识别的复盘 JSON，请重试或更换模型")
	}
	if err = validateAnalysis(result, view); err != nil {
		return domain.AIReview{}, err
	}
	result.Model, result.Created = config.Model, time.Now().UTC().Format(time.RFC3339)
	return result, nil
}
