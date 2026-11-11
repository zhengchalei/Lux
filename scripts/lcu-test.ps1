param(
    [ValidateSet('Inspect', 'ProbeFeatures', 'Schema', 'CreateClassic', 'CreateAram', 'CreateMayhem', 'Start', 'Hover', 'Cancel', 'Leave', 'ExitTestGame', 'CloseLux', 'MinimizeClient')]
    [string]$Action = 'Inspect',
    [int]$ChampionId = 99,
    [int[]]$GameProcessIds = @()
)

# lcu-test.ps1 exercises the user-authorized custom-room integration separately
# from Lux's explicitly armed selection transport. It discovers credentials inside an
# elevated process and writes only champion observations and request outcomes to
# the workspace artifacts directory. Authentication and player identities never
# enter the result files or child command arguments.
#
# Every room mutation requires the named Lux test lobby with no other human
# members. Creation requires an idle client, and the harness never starts public
# matchmaking. Each invocation performs one explicit action so the native window
# can be inspected between selection changes, then the test room can be left.

$ErrorActionPreference = 'Stop'
$workspacePath = Split-Path -Parent $PSScriptRoot
$reportPath = Join-Path $workspacePath "artifacts/live-test-$Action.json"
$testLobbyName = 'LuxTest'

# Invoke-Lcu performs one loopback request with process-local authentication.
# The caller supplies a constant protocol path. Redirects and proxies are disabled;
# Failed statuses retain protocol error codes and messages, without authentication
# headers or raw player responses entering the reports.
function Invoke-Lcu([string]$Method, [string]$Path, $Body = $null, [switch]$Optional) {
    $request = @{
        Uri = $script:lcuBase + $Path
        Method = $Method
        Headers = $script:lcuHeaders
        SkipCertificateCheck = $true
        SkipHttpErrorCheck = $true
        MaximumRedirection = 0
        NoProxy = $true
        TimeoutSec = 15
    }
    if ($null -ne $Body) {
        $request.Body = ConvertTo-Json -InputObject $Body -Depth 20 -Compress
        $request.ContentType = 'application/json'
    }
    $response = Invoke-WebRequest @request
    if ([int]$response.StatusCode -ge 400) {
        if ($Optional) { return $null }
        $failure = ConvertFrom-Json -InputObject $response.Content -AsHashtable -ErrorAction SilentlyContinue
        throw "Test request failed: $Method $Path (HTTP $([int]$response.StatusCode)); $($failure.errorCode) $($failure.message)"
    }
    if ([string]::IsNullOrWhiteSpace($response.Content)) { return $null }
    return ConvertFrom-Json -InputObject $response.Content -AsHashtable
}

# Assert-TestLobby checks ownership before a custom-room mutation is performed.
# The named lobby must be custom and contain only this user plus optional bots;
# an existing ordinary lobby or a room with another human is left untouched.
function Assert-TestLobby {
    $lobby = Invoke-Lcu GET '/lol-lobby/v2/lobby'
    if (!$lobby.gameConfig.isCustom -or $lobby.gameConfig.customLobbyName -ne $testLobbyName) {
        throw 'The current lobby is not the Lux integration test room'
    }
    $otherHumans = @($lobby.members | Where-Object { !$_.isBot -and $_.puuid -ne $lobby.localMember.puuid })
    if ($otherHumans.Count -gt 0) { throw 'The test room contains another human; mutation stopped' }
    return $lobby
}

# Get-TestObservation returns sanitized client state for the integration record.
# Friendly intent is retained to verify preview behavior. Enemy observations include
# only completed picks, and account identities and raw lobby responses are omitted.
function Get-TestObservation {
    $phase = Invoke-Lcu GET '/lol-gameflow/v1/gameflow-phase'
    $flow = Invoke-Lcu GET '/lol-gameflow/v1/session' -Optional
    $lobby = Invoke-Lcu GET '/lol-lobby/v2/lobby' -Optional
    $session = if ($phase -eq 'ChampSelect') { Invoke-Lcu GET '/lol-champ-select/v1/session' } else { $null }
    $actions = @($session.actions | ForEach-Object { $_ } | ForEach-Object { $_ })
    $publicEnemy = @($session.theirTeam | ForEach-Object {
        $member = $_
        $locked = @($actions | Where-Object { $_.actorCellId -eq $member.cellId -and $_.type -eq 'pick' -and $_.completed }).Count -gt 0
        @{cell=$member.cellId;champion=$(if ($locked) { $member.championId } else { 0 });locked=$locked}
    })
    return @{
        phase = $phase
        game = if ($flow) { @{id=$flow.gameData.gameId;isCustom=$flow.gameData.isCustomGame;queue=$flow.gameData.queue.id;mode=$flow.gameData.queue.gameMode;mapId=$flow.gameData.queue.mapId} } else { $null }
        lobby = if ($lobby) { @{isCustom=$lobby.gameConfig.isCustom;name=$lobby.gameConfig.customLobbyName;mapId=$lobby.gameConfig.mapId;mode=$lobby.gameConfig.gameMode;queue=$lobby.gameConfig.queueId;pickType=$lobby.gameConfig.pickType;members=@($lobby.members).Count;canStart=$lobby.canStartActivity} } else { $null }
        session = if ($session) { @{
            queue=$session.queueId;benchEnabled=$session.benchEnabled;localCell=$session.localPlayerCellId
            allies=@($session.myTeam | ForEach-Object { @{cell=$_.cellId;champion=$_.championId;intent=$_.championPickIntent;position=$_.assignedPosition} })
            enemies=$publicEnemy
            bench=@($session.benchChampions | ForEach-Object { $_.championId })
            localActions=@($actions | Where-Object { $_.actorCellId -eq $session.localPlayerCellId } | ForEach-Object { @{id=$_.id;type=$_.type;champion=$_.championId;completed=$_.completed;inProgress=$_.isInProgress} })
            pickableCount=@(Invoke-Lcu GET '/lol-champ-select/v1/pickable-champion-ids' -Optional).Count
        } } else { $null }
    }
}

try {
    if ($Action -eq 'CloseLux') {
        $luxPath = Join-Path $workspacePath 'dist/Lux-0.1.0-windows-x64/Lux.exe'
        Get-Process Lux -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $luxPath } | Stop-Process
        @{success=$true;action=$Action} | ConvertTo-Json | Set-Content -LiteralPath $reportPath -Encoding utf8
        exit 0
    }
    $client = Get-CimInstance Win32_Process -Filter "Name='LeagueClientUx.exe'" | Select-Object -First 1
    $portMatch = [regex]::Match($client.CommandLine, '--app-port=(\d+)')
    $tokenMatch = [regex]::Match($client.CommandLine, '--remoting-auth-token=([^\s"]+)')
    if (!$portMatch.Success -or !$tokenMatch.Success) { throw 'Client credentials are not readable in the test process' }
    $script:lcuBase = 'https://127.0.0.1:' + $portMatch.Groups[1].Value
    $script:lcuHeaders = @{Authorization='Basic ' + [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes('riot:' + $tokenMatch.Groups[1].Value))}
    $before = Get-TestObservation
    $protocol = $null
    switch ($Action) {
        'ProbeFeatures' {
            $own = Invoke-Lcu GET '/lol-summoner/v1/current-summoner'
            $aliases = Invoke-Lcu POST '/lol-summoner/v1/summoners/aliases' @(@{gameName=$own.gameName;tagLine=$own.tagLine}) -Optional
            $history = Invoke-Lcu GET "/lol-match-history/v1/products/lol/$($own.puuid)/matches?begIndex=0&endIndex=19"
            $played = $history.games.games | Where-Object { $_.queueId -lt 3000 -and $_.gameDuration -ge 180 } | Select-Object -First 1
            $detail = Invoke-Lcu GET "/lol-match-history/v1/games/$($played.gameId)"
            $opponent = $detail.participantIdentities | Where-Object { $_.player.puuid -ne $own.puuid -and $_.player.puuid } | Select-Object -First 1
            $other = Invoke-Lcu GET "/lol-summoner/v2/summoners/puuid/$($opponent.player.puuid)" -Optional
            $otherHistory = Invoke-Lcu GET "/lol-match-history/v1/products/lol/$($opponent.player.puuid)/matches?begIndex=0&endIndex=19" -Optional
            $ranked = Invoke-Lcu GET "/lol-ranked/v1/ranked-stats/$($own.puuid)" -Optional
            $protocol=@{aliasResolvedOwn=@($aliases | Where-Object {$_.puuid -eq $own.puuid}).Count -gt 0;ownGames=@($history.games.games).Count;reviewPlayers=@($detail.participants).Count;otherResolved=$null -ne $other;otherGames=@($otherHistory.games.games).Count;rankedKeys=@($ranked.Keys);queueMapKeys=@($ranked.queueMap.Keys)}
        }
        'ExitTestGame' {
            $flow = Invoke-Lcu GET '/lol-gameflow/v1/session'
            $players = @(@($flow.gameData.teamOne) + @($flow.gameData.teamTwo) | Where-Object { $null -ne $_ })
            $own = Invoke-Lcu GET '/lol-summoner/v1/current-summoner'
            # A timer may launch the single-player test automatically; exit is allowed only after matching the sole participant to this account.
            if (!$flow.gameData.isCustomGame -or $players.Count -ne 1 -or $players[0].puuid -ne $own.puuid) {
                throw "Exit requires a custom game containing only this account; observed participants: $($players.Count)"
            }
            if ($before.phase -eq 'Reconnect') {
                $protocol = @{cleanup='Waiting for the server to release this single-player custom game'}
                break
            }
            # This client's custom queues reject the early-exit API with REMOVAL_NOT_ALLOWED. Closing the verified sole participant stops its game processes; server gameflow may remain Reconnect.
            $testGamePath = Join-Path (Split-Path -Parent (Split-Path -Parent $client.ExecutablePath)) 'Game/League of Legends.exe'
            $gameCandidates = @(Get-CimInstance Win32_Process -Filter "Name='League of Legends.exe'")
            $gameProcesses = @($gameCandidates | Where-Object { $_.ExecutablePath -eq $testGamePath })
            # Protected game processes may omit executable paths even when elevated. Explicit IDs from the native window inventory restrict cleanup to observed test instances.
            if ($gameProcesses.Count -eq 0 -and $GameProcessIds.Count -gt 0) {
                $gameProcesses = @($gameCandidates | Where-Object { $_.ProcessId -in $GameProcessIds })
            }
            if ($gameProcesses.Count -eq 0) { throw "No game process at $testGamePath was found; visible paths: $($gameCandidates.ExecutablePath -join ', ')" }
            $gameProcesses | ForEach-Object { Stop-Process -Id $_.ProcessId }
        }
        'MinimizeClient' { $null = Invoke-Lcu POST '/riotclient/ux-minimize' }
        'Schema' {
            $schema = Invoke-Lcu GET '/help?format=Full'
            $protocol = @{}
            $protocol.definitions=@{}
            $protocol.gameFunctions=@($schema.functions | Where-Object { $_.name -match 'EarlyExit|Quit|Terminate|UxMinimize|GameflowV1Session' })
            foreach ($name in @('PostLolLobbyV2Lobby','PostLolLobbyV1LobbyCustomBots','PostLolLobbyV1LobbyCustomStartChampSelect','GetLolGameQueuesV1Custom','GetLolGameQueuesV1CustomNonDefault')) {
                $protocol.definitions[$name]=$schema.functions | Where-Object { $_.name -eq $name }
            }
            $protocol.types=@{}
            foreach ($type in @($schema.types | Where-Object { $_.name -match 'Lobby.*(Custom|Configuration|Create|Change|Bot)|LobbyCustom|CustomGame' })) {
                $protocol.types[$type.name]=$type
            }
            $protocol.custom=Invoke-Lcu GET '/lol-game-queues/v1/custom' -Optional
            $protocol.nonDefault=Invoke-Lcu GET '/lol-game-queues/v1/custom-non-default' -Optional
            $protocol.queues=@(Invoke-Lcu GET '/lol-game-queues/v1/queues' | Where-Object { $_.id -in 2400,450,3100,3220,3230,3270,3210,3200 })
            $protocol.botCatalog=Invoke-Lcu GET '/lol-lobby/v2/lobby/custom/available-bots' -Optional
        }
        { $_ -in 'CreateClassic', 'CreateAram', 'CreateMayhem' } {
            if ($before.phase -ne 'None') { throw 'Custom-room creation requires an idle client' }
            $customQueue = switch ($Action) { 'CreateClassic' {3100} 'CreateAram' {3200} 'CreateMayhem' {3270} }
            $queue = Invoke-Lcu GET '/lol-game-queues/v1/queues' | Where-Object { $_.id -eq $customQueue -and $_.isCustom }
            if (!$queue -or $queue.queueAvailability -ne 'Available') { throw 'The requested test queue is not available' }
            $configuration = Invoke-Lcu GET '/lol-game-queues/v1/custom'
            $category = $configuration.subcategories | Where-Object { @($_.mutators.id) -contains $customQueue } | Select-Object -First 1
            if (!$category -or @($category.mutators).Count -eq 0) { throw 'No available custom mode was published' }
            # Current clients publish mode-specific mutator identifiers; legacy Akari's fixed id 1 is rejected as INVALID_LOBBY here.
            $mutator = $category.mutators | Where-Object { $_.id -eq $customQueue }
            $null = Invoke-Lcu POST '/lol-lobby/v2/lobby' @{
                queueId=$customQueue
                customGameLobby=@{
                    lobbyName=$testLobbyName;lobbyPassword='Lux123'
                    configuration=@{gameMode=$queue.gameMode;gameMutator='';mapId=$queue.mapId;mutators=$mutator;gameTypeConfig=$queue.gameTypeConfig;spectatorPolicy='NotAllowed';teamSize=5;maxPlayerCount=10;hidePublicly=$true}
                }
            }
        }
        'Start' {
            $null = Assert-TestLobby
            $startResponse = Invoke-Lcu POST '/lol-lobby/v1/lobby/custom/start-champ-select'
            $protocol = @{startStatus=$startResponse.status;startError=$startResponse.errorCode;startMessage=$startResponse.message;startFields=@($startResponse.Keys)}
        }
        'Hover' {
            $null = Assert-TestLobby
            if ($before.phase -ne 'ChampSelect') { throw 'Hover requires champion selection' }
            $actionRow = $before.session.localActions | Where-Object { $_.type -eq 'pick' -and !$_.completed } | Select-Object -First 1
            if (!$actionRow) { throw 'No pending local pick action is present' }
            $null = Invoke-Lcu PATCH "/lol-champ-select/v1/session/actions/$($actionRow.id)" @{championId=$ChampionId}
        }
        'Leave' {
            $null = Assert-TestLobby
            $null = Invoke-Lcu DELETE '/lol-lobby/v2/lobby'
        }
        'Cancel' {
            $null = Assert-TestLobby
            $null = Invoke-Lcu POST '/lol-lobby/v1/lobby/custom/cancel-champ-select'
        }
    }
    # The start endpoint may return before gameflow enters selection. Observe the requested phase before allowing the next harness invocation to choose a hero.
    if ($Action -notin 'Inspect', 'Schema') { Start-Sleep -Milliseconds 1200 }
    $after = Get-TestObservation
    if ($Action -eq 'Start') {
        for ($attempt = 0; $attempt -lt 20 -and $after.phase -eq 'Lobby'; $attempt++) {
            Start-Sleep -Milliseconds 250
            $after = Get-TestObservation
        }
        if ($after.phase -ne 'ChampSelect') { throw "Selection did not open; current phase: $($after.phase)" }
    }
    @{success=$true;action=$Action;before=$before;after=$after;protocol=$protocol} |
        ConvertTo-Json -Depth 15 | Set-Content -LiteralPath $reportPath -Encoding utf8
} catch {
    @{success=$false;action=$Action;message=$_.Exception.Message;before=$before;after=$after;protocol=$protocol} | ConvertTo-Json -Depth 10 |
        Set-Content -LiteralPath $reportPath -Encoding utf8
    exit 1
}
