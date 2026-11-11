package updater

import (
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// github.go reads Lux's stable GitHub release and verifies its executable asset.
// Version comparison uses numeric components rather than text order, and release
// metadata must identify this repository's Lux.exe with a GitHub SHA-256 digest.
// Downloads remain separate from the running executable until size, digest, and
// Windows architecture checks have all passed. Missing or interrupted content
// cannot be offered to the installer as a ready update.
//
// Only public GitHub hosts participate in requests, including GitHub's asset CDN
// redirects. No LCU credentials or player records enter these requests. Each
// caller supplies cancellation and owns its destination file, allowing the
// service to revoke a download without modifying the installed application.

// GitHub endpoints and byte limits bound public metadata and executable downloads for this Windows x64 application.
const (
	latestURL = "https://api.github.com/repos/zhengchalei/Lux/releases/latest"
	maxAsset  = 128 << 20
)

// release represents the stable GitHub API fields needed for one executable update.
type release struct {
	Tag        string  `json:"tag_name"`   // Published numeric version tag.
	Draft      bool    `json:"draft"`      // Unpublished draft release marker.
	Prerelease bool    `json:"prerelease"` // Explicit preview release marker.
	Assets     []asset `json:"assets"`     // Public release download entries.
}

// asset binds the downloadable executable to its expected byte count and digest.
type asset struct {
	Name   string `json:"name"`                 // Published release asset name.
	URL    string `json:"browser_download_url"` // Repository-scoped public download URL.
	Size   int64  `json:"size"`                 // Expected file size in bytes.
	Digest string `json:"digest"`               // GitHub-provided SHA-256 digest string.
}

// version parses the stable three-component versions used by Lux releases.
// Preview labels, omitted components, and negative values are rejected, so an
// unexpected tag cannot override an installed stable version through text ordering.
func version(text string) ([3]uint64, error) {
	var result [3]uint64
	parts := strings.Split(strings.TrimPrefix(text, "v"), ".")
	if len(parts) != 3 {
		return result, errors.New("版本号格式不受支持")
	}
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return result, errors.New("版本号格式不受支持")
		}
		result[i] = value
	}
	return result, nil
}

// newer compares complete stable numeric versions without treating 0.2.9 as newer than 0.2.10.
func newer(candidate, current string) bool {
	a, err := version(candidate)
	if err != nil {
		return false
	}
	b, err := version(current)
	if err != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// trustedURL limits metadata, asset requests, and redirects to GitHub's HTTPS hosts.
func trustedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

// request makes one public request with explicit product headers and HTTP error reporting.
// The caller owns the returned body. Client-level redirects remain subject to
// the service's host policy, and supplied contexts bound the entire request.
func request(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	if !trustedURL(address) {
		return nil, errors.New("更新地址不属于 GitHub")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Lux-Windows-Updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接 GitHub：%w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub 更新请求失败（HTTP %d）", response.StatusCode)
	}
	return response, nil
}

// latest resolves the stable executable and rejects missing, untrusted, or unverified assets.
// Older releases containing only a ZIP are treated as unavailable. A newer tag
// must publish a bounded Lux.exe whose URL matches that exact repository tag.
// Tags that cannot advance the installed numeric version never trigger a download.
// Metadata supplies both size and digest together, so a partially published asset
// cannot be confused with a complete executable from another release or repository.
func latest(ctx context.Context, client *http.Client, current string) (string, *asset, error) {
	response, err := request(ctx, client, latestURL)
	if err != nil {
		return "", nil, err
	}
	defer response.Body.Close()
	var entry release
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&entry); err != nil {
		return "", nil, errors.New("GitHub 更新信息无法读取")
	}
	if entry.Draft || entry.Prerelease {
		return "", nil, errors.New("GitHub 返回了非正式版本")
	}
	if _, err = version(entry.Tag); err != nil {
		return "", nil, err
	}
	if !newer(entry.Tag, current) {
		return current, nil, nil
	}
	for _, item := range entry.Assets {
		if item.Name != "Lux.exe" {
			continue
		}
		expected := "https://github.com/zhengchalei/Lux/releases/download/" + entry.Tag + "/Lux.exe"
		checksum, err := hex.DecodeString(strings.TrimPrefix(item.Digest, "sha256:"))
		if item.URL != expected || !strings.HasPrefix(item.Digest, "sha256:") || err != nil || len(checksum) != sha256.Size || item.Size <= 0 || item.Size > maxAsset {
			return "", nil, errors.New("新版 EXE 的地址、大小或校验信息不完整")
		}
		return strings.TrimPrefix(entry.Tag, "v"), &item, nil
	}
	return "", nil, errors.New("新版尚未提供 Lux.exe，请稍后重试")
}

// download writes one bounded candidate and checks its public GitHub digest.
// A failed operation removes partial content. Progress reports bytes received,
// while only a fully verified Windows x64 executable survives successful return.
// The destination must not already exist, preserving any prior ready candidate.
// Byte counting and hashing share the file writer, so the checksum describes the
// exact saved payload. The writer closes before PE inspection to allow Windows
// to reopen the candidate, and cancellation remains owned by the supplied context.
func download(ctx context.Context, client *http.Client, item asset, path string, progress func(int)) (err error) {
	response, err := request(ctx, client, item.URL)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	hash := sha256.New()
	writer := &progressWriter{writer: io.MultiWriter(file, hash), total: item.Size, report: progress, last: -1}
	written, err := io.Copy(writer, io.LimitReader(response.Body, item.Size+1))
	if err != nil {
		return err
	}
	if written != item.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != item.Digest {
		return errors.New("新版 EXE 大小或 SHA-256 校验失败")
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return verifyExecutable(path)
}

// progressWriter coalesces download observations to changed whole percentages for the settings page.
type progressWriter struct {
	writer io.Writer // Destination and digest writer.
	total  int64     // Expected complete byte count.
	count  int64     // Received asset byte count.
	last   int       // Previously reported whole percentage.
	report func(int) // Optional bounded progress callback.
}

// Write forwards bytes and publishes only changed, bounded download percentages.
func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.count += int64(n)
	percent := min(100, int(w.count*100/w.total))
	if percent != w.last && w.report != nil {
		w.last = percent
		w.report(percent)
	}
	return n, err
}

// verifyExecutable requires an AMD64 Windows GUI executable with a real code section.
func verifyExecutable(path string) error {
	file, err := pe.Open(path)
	if err != nil {
		return errors.New("下载内容不是有效的 Windows 程序")
	}
	defer file.Close()
	optional, ok := file.OptionalHeader.(*pe.OptionalHeader64)
	if !ok || file.FileHeader.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || file.FileHeader.Characteristics&pe.IMAGE_FILE_DLL != 0 || optional.Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI || file.Section(".text") == nil {
		return errors.New("下载程序不是 Windows x64 Lux 可执行文件")
	}
	return nil
}
