# remode

모바일 Telegram / Discord에서 로컬 맥의 tmux 세션으로 실행 중인 AI 코딩 에이전트를 원격 제어하는 봇 프레임워크.

**지원 에이전트:** Claude Code, Codex  
**지원 플랫폼:** Telegram, Discord, Web (REST API + SSE)

## 동작 방식

```
[Telegram / Discord / Web UI]
          │
        remode
          │
    ┌─────┴──────┐
    │            │
  tmux      Scheduler / Orchestrator
    │
Claude Code / Codex
    │
    └── JSONL tail / tmux capture ──► 채팅 / SSE 스트림
```

- 채팅 메시지 → `tmux send-keys` 로 에이전트에 전달
- 에이전트 출력(JSONL) → fsnotify tail 로 파싱 → 채팅 / SSE로 전송
- 승인 다이얼로그 / Plan 배너 → `tmux capture-pane` 폴링으로 감지 → 버튼으로 전송
- 스케줄러 → cron 기반으로 세션에 프롬프트 전송 / 상태 보고 / 배치 세션 실행
- 오케스트레이터 → 여러 세션에 브로드캐스트 / 체인 / DAG 워크플로우 실행

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

[web]
enabled     = false
listen_addr = "127.0.0.1:8765"
auth_token  = ""                 # Bearer 토큰; 비어 있으면 Web 서버 시작 안 함

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
turn_idle_ms        = 4000            # 오케스트레이터가 turn 완료로 판단하는 무음 시간(ms)
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
| `/resend` | 마지막으로 전송된 응답 메시지 재전송 |
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

## Web API

`[web] enabled = true` 로 켜면 HTTP REST 서버가 `listen_addr` 에서 실행됩니다.  
모든 요청에 `Authorization: Bearer <auth_token>` 헤더가 필요합니다.

### 세션

| 메서드 | 경로 | 설명 |
|--------|------|------|
| `GET` | `/api/sessions` | 세션 목록 |
| `GET` | `/api/sessions/{name}` | 세션 상세 + 화면 캡처 |
| `POST` | `/api/sessions` | 세션 생성 `{"name","cwd","agent_type"}` |
| `POST` | `/api/sessions/{name}/input` | 텍스트 입력 전송 |
| `POST` | `/api/sessions/{name}/key` | 키 전송 (예: `Enter`, `C-c`) |
| `POST` | `/api/sessions/{name}/kill` | 세션 종료 |
| `POST` | `/api/sessions/{name}/level` | 메시지 수준 변경 |
| `GET` | `/api/sessions/{name}/stream` | SSE 출력 스트림 |
| `GET` | `/api/projects` | 프로젝트 목록 |

### 스케줄

| 메서드 | 경로 | 설명 |
|--------|------|------|
| `GET` | `/api/schedules` | 스케줄 목록 |
| `POST` | `/api/schedules` | 스케줄 생성 |
| `GET` | `/api/schedules/{id}` | 스케줄 상세 |
| `PUT` | `/api/schedules/{id}` | 스케줄 수정 |
| `DELETE` | `/api/schedules/{id}` | 스케줄 삭제 |
| `POST` | `/api/schedules/{id}/fire` | 스케줄 즉시 실행 |

### 워크플로우 (DAG)

| 메서드 | 경로 | 설명 |
|--------|------|------|
| `GET` | `/api/workflows` | 워크플로우 목록 |
| `POST` | `/api/workflows` | 워크플로우 생성 |
| `GET` | `/api/workflows/{id}` | 워크플로우 상세 |
| `PUT` | `/api/workflows/{id}` | 워크플로우 수정 |
| `DELETE` | `/api/workflows/{id}` | 워크플로우 삭제 |
| `POST` | `/api/workflows/{id}/run` | 워크플로우 실행 |
| `GET` | `/api/workflows/{id}/runs` | 실행 이력 조회 |
| `GET` | `/api/runs/{runID}` | 실행 상세 조회 |

### 오케스트레이션

| 메서드 | 경로 | 설명 |
|--------|------|------|
| `POST` | `/api/broadcast` | 여러 세션에 동시 프롬프트 전송 |
| `POST` | `/api/chain` | 세션 A 완료 후 출력을 세션 B로 전달 |

## 스케줄러

cron 기반으로 세션 작업을 자동화합니다. `cron_spec`은 6-필드(초 포함) robfig/cron v3 형식입니다.

| 액션 타입 | 설명 |
|-----------|------|
| `send_prompt` | 지정 세션에 미리 정의된 프롬프트 전송 |
| `status_report` | 세션 화면을 캡처해 메시지로 전달 |
| `batch_session` | 세션 생성 → 프롬프트 실행 → 마감 시간 후 종료 |

## 워크플로우 (DAG 오케스트레이션)

세션 노드를 DAG(방향 비순환 그래프)로 연결해 복잡한 다단계 작업을 자동화합니다.

- **노드**: 기존 세션 또는 `SessionTemplate`으로 새로 생성한 세션에 프롬프트 전송
- **엣지**: 노드 간 의존 관계 선언 (Kahn 알고리즘으로 위상 정렬 실행)
- **상태 추적**: `pending → running → done / failed / skipped`
- **재개**: 프로세스 재시작 후에도 `running` 상태의 런을 자동으로 재개

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
    web/             HTTP REST + SSE 관리 서버
  orchestrator/      브로드캐스트 / 체인 / DAG 워크플로우 실행 엔진
  scheduler/         cron 기반 스케줄러
  session/           세션 생명주기 관리
  formatter/         AgentEvent → Message 변환
  store/
    sqlite/          SQLite 세션·스케줄·워크플로우 저장소
    memory/          인메모리 저장소 (테스트용)
  config/            TOML 설정 로더
```

## 빌드

```bash
go build .
go test ./...
```
