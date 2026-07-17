package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/term"

	"localcode/internal/awssso"
	"localcode/internal/credentials"
)

// runLogin dispatches "localcode login <target>" to the anthropic or
// bedrock flow. Both talk directly to stdin/stdout (this runs before any
// TUI takes over the screen, same as pickOrCreateSession's prompt).
func runLogin(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: localcode login <anthropic|bedrock>")
	}
	switch args[0] {
	case "anthropic":
		return loginAnthropic()
	case "bedrock":
		return loginBedrock(args[1:])
	default:
		return fmt.Errorf("unknown login target %q (want \"anthropic\" or \"bedrock\")", args[0])
	}
}

// --- Anthropic direct API key ---

func loginAnthropic() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	fmt.Println("Anthropic API 키를 입력하세요 (console.anthropic.com > API Keys에서 발급).")
	fmt.Println("이 키는 Anthropic API 사용량만큼 별도 과금되며, claude.ai Pro/Max 구독과는 무관합니다.")
	fmt.Print("API 키: ")

	key, err := readSecret()
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("빈 API 키는 저장할 수 없습니다")
	}
	if !strings.HasPrefix(key, "sk-ant-") {
		fmt.Println("경고: 일반적인 Anthropic API 키는 \"sk-ant-\"로 시작합니다 — 그래도 입력한 값을 저장합니다.")
	}

	if err := credentials.SaveAnthropicAPIKey(home, key); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}

	fmt.Println()
	fmt.Println("저장 완료: ~/.localcode/credentials.json")
	fmt.Println(`config.json에 아래와 같이 provider를 추가하세요 (api_key 필드는 생략 가능 — 저장된 키를 자동으로 사용합니다):`)
	fmt.Println(`  "providers": { "anthropic": { "type": "anthropic" } }`)
	return nil
}

// readSecret reads one line from stdin without echoing it, when stdin is a
// real terminal; falls back to a plain (echoed) read otherwise — e.g. when
// piped in a test or a non-interactive script.
func readSecret() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return line, nil
}

// --- AWS Bedrock via SSO device authorization ---

func loginBedrock(args []string) error {
	fs := flag.NewFlagSet("login bedrock", flag.ContinueOnError)
	startURL := fs.String("start-url", "", "AWS IAM Identity Center start URL (e.g. https://my-sso.awsapps.com/start)")
	ssoRegion := fs.String("sso-region", "", "region IAM Identity Center itself runs in (e.g. us-east-1)")
	region := fs.String("region", "", "region Bedrock calls should use (defaults to --sso-region if left blank)")
	profileName := fs.String("profile", "localcode-bedrock", "AWS profile name to write to ~/.aws/config")
	accountID := fs.String("account", "", "AWS account ID to pin the profile to (skips the interactive picker)")
	roleName := fs.String("role", "", "SSO role name to pin the profile to (skips the interactive picker)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reader := bufio.NewReader(os.Stdin)

	if *startURL == "" {
		fmt.Print("AWS SSO 시작 URL (예: https://my-sso.awsapps.com/start): ")
		line, _ := reader.ReadString('\n')
		*startURL = strings.TrimSpace(line)
	}
	if *startURL == "" {
		return fmt.Errorf("시작 URL이 필요합니다")
	}
	if *ssoRegion == "" {
		fmt.Print("SSO 리전 (예: us-east-1): ")
		line, _ := reader.ReadString('\n')
		*ssoRegion = strings.TrimSpace(line)
	}
	if *ssoRegion == "" {
		return fmt.Errorf("SSO 리전이 필요합니다")
	}
	if *region == "" {
		*region = *ssoRegion
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	ctx := context.Background()
	tok, err := awssso.Login(ctx, *startURL, *ssoRegion, func(auth awssso.DeviceAuth) {
		fmt.Println()
		fmt.Println("브라우저에서 아래 URL을 열어 로그인을 완료하세요:")
		fmt.Println("  " + auth.VerificationURIComplete)
		if auth.UserCode != "" {
			fmt.Println("  (코드가 자동 입력되지 않으면 직접 입력: " + auth.UserCode + ")")
		}
		if err := openBrowser(auth.VerificationURIComplete); err != nil {
			fmt.Println("(브라우저 자동 실행 실패 — 위 URL을 직접 열어주세요)")
		}
		fmt.Println("승인을 기다리는 중...")
	})
	if err != nil {
		return fmt.Errorf("SSO 로그인 실패: %w", err)
	}
	fmt.Println("로그인 성공.")

	if *accountID == "" {
		accounts, err := awssso.ListAccounts(ctx, *ssoRegion, tok.AccessToken)
		if err != nil {
			return fmt.Errorf("list accounts: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("이 SSO 로그인으로 접근 가능한 AWS 계정이 없습니다")
		}
		if len(accounts) == 1 {
			*accountID = accounts[0].ID
			fmt.Printf("계정 자동 선택: %s (%s)\n", accounts[0].Name, accounts[0].ID)
		} else {
			fmt.Println("사용 가능한 AWS 계정:")
			for i, a := range accounts {
				fmt.Printf("  [%d] %s (%s)\n", i+1, a.Name, a.ID)
			}
			fmt.Print("선택 (번호): ")
			line, _ := reader.ReadString('\n')
			idx, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr != nil || idx < 1 || idx > len(accounts) {
				return fmt.Errorf("잘못된 선택입니다")
			}
			*accountID = accounts[idx-1].ID
		}
	}

	if *roleName == "" {
		roles, err := awssso.ListAccountRoles(ctx, *ssoRegion, tok.AccessToken, *accountID)
		if err != nil {
			return fmt.Errorf("list account roles: %w", err)
		}
		if len(roles) == 0 {
			return fmt.Errorf("계정 %s에서 사용 가능한 SSO 역할이 없습니다", *accountID)
		}
		if len(roles) == 1 {
			*roleName = roles[0].Name
			fmt.Printf("역할 자동 선택: %s\n", roles[0].Name)
		} else {
			fmt.Println("사용 가능한 역할:")
			for i, r := range roles {
				fmt.Printf("  [%d] %s\n", i+1, r.Name)
			}
			fmt.Print("선택 (번호): ")
			line, _ := reader.ReadString('\n')
			idx, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr != nil || idx < 1 || idx > len(roles) {
				return fmt.Errorf("잘못된 선택입니다")
			}
			*roleName = roles[idx-1].Name
		}
	}

	if err := awssso.WriteTokenCache(home, *startURL, *ssoRegion, tok); err != nil {
		return fmt.Errorf("save SSO token cache: %w", err)
	}
	if err := awssso.WriteProfile(home, *profileName, *startURL, *ssoRegion, *accountID, *roleName, *region); err != nil {
		return fmt.Errorf("write AWS profile: %w", err)
	}

	fmt.Println()
	fmt.Printf("저장 완료: ~/.aws/config에 [profile %s], ~/.aws/sso/cache에 토큰 캐시\n", *profileName)
	fmt.Println(`config.json에 아래와 같이 provider를 추가하세요:`)
	fmt.Printf("  \"providers\": { \"bedrock\": { \"type\": \"bedrock\", \"region\": %q, \"profile\": %q } }\n", *region, *profileName)
	return nil
}

// openBrowser is a best-effort convenience — device-flow login doesn't
// require the browser to be on this machine at all (the URL/code work
// from any device), so a failure here is not fatal, only less convenient.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
