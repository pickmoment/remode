package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Setup runs an interactive wizard and writes a config.toml to path.
func Setup(path string) error {
	r := bufio.NewReader(os.Stdin)
	ask := func(prompt, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", prompt, def)
		} else {
			fmt.Printf("%s: ", prompt)
		}
		line, _ := r.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}
	askRequired := func(prompt string) string {
		for {
			v := ask(prompt, "")
			if v != "" {
				return v
			}
			fmt.Println("  필수 항목입니다.")
		}
	}
	parseIDs := func(s string) []int64 {
		var ids []int64
		for _, part := range strings.Split(s, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if id, err := strconv.ParseInt(part, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	}

	fmt.Println()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  remode 초기 설정")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println()

	// Platform
	fmt.Println("1. 플랫폼을 선택하세요.")
	fmt.Println("   1) Telegram")
	fmt.Println("   2) Discord")
	platformChoice := ask("선택", "1")
	platform := "telegram"
	if platformChoice == "2" || strings.EqualFold(platformChoice, "discord") {
		platform = "discord"
	}

	// Token
	fmt.Println()
	token := askRequired("2. 봇 토큰을 입력하세요")

	// Allowed user IDs
	fmt.Println()
	fmt.Println("3. 허용할 사용자 ID를 입력하세요. (쉼표로 구분, 예: 123456789,987654321)")
	fmt.Println("   Telegram: @userinfobot 으로 확인 / Discord: 개발자 모드에서 사용자 우클릭 → ID 복사")
	allowedStr := ask("사용자 ID", "")
	allowedIDs := parseIDs(allowedStr)

	// Discord notify channels (optional)
	var notifyChannelIDs []int64
	if platform == "discord" {
		fmt.Println()
		notifyStr := ask("4. 봇 재시작 알림을 받을 채널 ID (선택, 쉼표로 구분)", "")
		notifyChannelIDs = parseIDs(notifyStr)
	}

	// Agents
	fmt.Println()
	fmt.Println("5. 사용할 에이전트를 선택하세요.")
	fmt.Println("   1) Claude Code")
	fmt.Println("   2) Codex")
	fmt.Println("   3) 둘 다")
	agentChoice := ask("선택", "1")
	var agents []string
	switch agentChoice {
	case "2":
		agents = []string{"codex"}
	case "3":
		agents = []string{"claude_code", "codex"}
	default:
		agents = []string{"claude_code"}
	}

	// Paths (defaults)
	home, _ := os.UserHomeDir()
	fmt.Println()
	fmt.Println("6. 경로 설정 (엔터로 기본값 사용)")
	newProjectDir := ask("새 프로젝트 기본 경로", filepath.Join(home, "projects"))

	// Write config
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("설정 디렉터리 생성 실패: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(`platform = "` + platform + `"` + "\n\n")

	if platform == "telegram" {
		sb.WriteString("[telegram]\n")
		sb.WriteString(`token = "` + token + `"` + "\n")
		sb.WriteString("allowed_user_ids = " + int64SliceTOML(allowedIDs) + "\n\n")
	} else {
		sb.WriteString("[discord]\n")
		sb.WriteString(`token = "` + token + `"` + "\n")
		sb.WriteString("allowed_user_ids = " + int64SliceTOML(allowedIDs) + "\n")
		sb.WriteString("notify_channel_ids = " + int64SliceTOML(notifyChannelIDs) + "\n\n")
	}

	sb.WriteString("[agents]\n")
	sb.WriteString("enabled = " + strSliceTOML(agents) + "\n\n")

	sb.WriteString("[paths]\n")
	sb.WriteString(`new_project_dir = "` + newProjectDir + `"` + "\n\n")

	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		return fmt.Errorf("설정 파일 저장 실패: %w", err)
	}

	fmt.Println()
	fmt.Printf("✅ 설정이 저장됐습니다: %s\n\n", path)
	return nil
}

func int64SliceTOML(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func strSliceTOML(ss []string) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = `"` + s + `"`
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
