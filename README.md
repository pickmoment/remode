# remode

모바일 Telegram / Discord에서 로컬 맥의 tmux 세션으로 실행 중인 AI 코딩 에이전트를 원격 제어하는 봇 프레임워크.

**지원 에이전트:** Claude Code, Codex  
**지원 플랫폼:** Telegram, Discord

## 동작 방식

```
[Telegram / Discord] ──── remode ──── tmux ──── Claude Code / Codex
        ↑                                              │
        └──────────── JSONL tail / tmux capture ───────┘
```

- 채팅 메시지 → `tmux send-keys` 로 에이전트에 전달
- 에이전트 출력(JSONL) → fsnotify tail 로 파싱 → 채팅으로 전송
- 승인 다이얼로그 / Plan 배너 → `tmux capture-pane` 폴링으로 감지 → 버튼으로 전송

## 요구 사항

- Go 1.23+
- tmux
- Claude Code CLI (`claude`) 또는 Codex CLI (`codex`)

## 설치

**소스 없이 바로 설치 (권장)**

```bash
go install github.com/pickmoment/remode@latest
```

**소스를 받아서 설치**

```bash
git clone https://github.com/pickmoment/remode.git
cd remode
go install .
```

## 설정

`~/.remode/config.toml` (또는 `REMODE_CONFIG` 환경변수로 경로 지정)

```toml
platform = "telegram"   # 또는 "discord"

[telegram]
token            = "BOT_TOKEN"
allowed_user_ids = [123456789]

[discord]
token              = "BOT_TOKEN"
allowed_user_ids   = [123456789]
notify_channel_ids = []          # 봇 재시작 알림을 받을 채널 ID

[agents]
enabled = ["claude_code"]        # "claude_code", "codex", 또는 둘 다

[paths]
db                  = "~/.remode/sessions.db"
new_project_dir     = "~/projects"
claude_projects_dir = "~/.claude/projects"
sessions_dir        = "~/.remode/sessions"

[tmux]
session_prefix = "R-"   # 세션 이름: R-CL-<name> (Claude Code), R-CX-<name> (Codex)

[monitor]
plan_banner_poll_ms = 500
jsonl_settle_ms     = 100
message_level       = "interactive"   # all | interactive | final
```

## 실행

```bash
remode                              # 포그라운드 실행
remode --tmux                       # tmux 세션 'remode'에서 백그라운드 실행
remode --config /path/to/config.toml
```

`--tmux` 플래그를 사용하면 `remode`라는 이름의 detached tmux 세션을 생성하고 즉시 리턴합니다. 세션이 이미 존재하면 아무것도 하지 않습니다.

## 명령어

| 명령어 | 설명 |
|--------|------|
| `/new <이름>` | 새 세션 생성 |
| `/attach [이름]` | 기존 세션에 채널 연결 |
| `/list` | 활성 세션 목록 |
| `/kill [이름]` | 세션 종료 |
| `/status` | 현재 세션 정보 |
| `/projects` | 에이전트 프로젝트 목록 조회 및 재개 |
| `/sessions` | 같은 프로젝트의 다른 세션으로 전환 |
| `/send <텍스트>` | 슬래시 커맨드 전달 (예: `/send /plan`) |
| `/clear` | 에이전트 대화 히스토리 초기화 |
| `/screen` | tmux 화면 캡처 |
| `/level [값]` | 메시지 수준 조회/변경 |
| `/shutdown` | 봇 프로세스 종료 |

일반 텍스트 메시지는 바인딩된 세션으로 그대로 전달됩니다.  
`\`로 시작하는 메시지는 앞의 백슬래시를 제거하고 전달합니다 (슬래시 커맨드 직접 입력용).

### 메시지 수준

| 수준 | 표시 내용 |
|------|-----------|
| `all` | 모든 이벤트 (텍스트, 툴 사용, 승인 버튼) |
| `interactive` | 어시스턴트 텍스트 + 승인/플랜 버튼 (기본값) |
| `final` | 어시스턴트 최종 답변만 |

## 프로젝트 구조

```
main.go              진입점
internal/
  core/              도메인 타입 및 인터페이스 (외부 의존성 없음)
  agent/
    claudecode/      Claude Code 에이전트 (tmux + JSONL + fsnotify)
    codex/           Codex 에이전트 (tmux + JSONL 폴링)
  platform/
    telegram/        Telegram 봇 구현
    discord/         Discord 봇 구현
  session/           세션 생명주기 관리
  formatter/         AgentEvent → Message 변환
  store/
    sqlite/          SQLite 세션 저장소
    memory/          인메모리 저장소 (테스트용)
  config/            TOML 설정 로더
```

## 빌드

```bash
go build .
go test ./...
```
