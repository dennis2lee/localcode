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

	fmt.Println("Enter your Anthropic API key (from console.anthropic.com > API Keys).")
	fmt.Println("This key is billed separately by Anthropic API usage, unrelated to a claude.ai Pro/Max subscription.")
	fmt.Print("API key: ")

	key, err := readSecret()
	if err != nil {
		return fmt.Errorf("read API key: %w", err)
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("cannot save an empty API key")
	}
	if !strings.HasPrefix(key, "sk-ant-") {
		fmt.Println("warning: a typical Anthropic API key starts with \"sk-ant-\" — saving the entered value anyway.")
	}

	if err := credentials.SaveAnthropicAPIKey(home, key); err != nil {
		return fmt.Errorf("save API key: %w", err)
	}

	fmt.Println()
	fmt.Println("Saved to: ~/.localcode/credentials.json")
	fmt.Println(`Add this provider to config.json (the api_key field can be omitted — the saved key is used automatically):`)
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
		fmt.Print("AWS SSO start URL (e.g. https://my-sso.awsapps.com/start): ")
		line, _ := reader.ReadString('\n')
		*startURL = strings.TrimSpace(line)
	}
	if *startURL == "" {
		return fmt.Errorf("a start URL is required")
	}
	if *ssoRegion == "" {
		fmt.Print("SSO region (e.g. us-east-1): ")
		line, _ := reader.ReadString('\n')
		*ssoRegion = strings.TrimSpace(line)
	}
	if *ssoRegion == "" {
		return fmt.Errorf("an SSO region is required")
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
		fmt.Println("Open this URL in a browser to finish logging in:")
		fmt.Println("  " + auth.VerificationURIComplete)
		if auth.UserCode != "" {
			fmt.Println("  (if the code isn't filled in automatically, enter it yourself: " + auth.UserCode + ")")
		}
		if err := openBrowser(auth.VerificationURIComplete); err != nil {
			fmt.Println("(couldn't open a browser automatically — open the URL above yourself)")
		}
		fmt.Println("Waiting for approval...")
	})
	if err != nil {
		return fmt.Errorf("SSO login failed: %w", err)
	}
	fmt.Println("Login succeeded.")

	if *accountID == "" {
		accounts, err := awssso.ListAccounts(ctx, *ssoRegion, tok.AccessToken)
		if err != nil {
			return fmt.Errorf("list accounts: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("no AWS accounts are reachable with this SSO login")
		}
		if len(accounts) == 1 {
			*accountID = accounts[0].ID
			fmt.Printf("Auto-selected account: %s (%s)\n", accounts[0].Name, accounts[0].ID)
		} else {
			fmt.Println("Available AWS accounts:")
			for i, a := range accounts {
				fmt.Printf("  [%d] %s (%s)\n", i+1, a.Name, a.ID)
			}
			fmt.Print("Choose (number): ")
			line, _ := reader.ReadString('\n')
			idx, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr != nil || idx < 1 || idx > len(accounts) {
				return fmt.Errorf("invalid choice")
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
			return fmt.Errorf("no SSO roles are available on account %s", *accountID)
		}
		if len(roles) == 1 {
			*roleName = roles[0].Name
			fmt.Printf("Auto-selected role: %s\n", roles[0].Name)
		} else {
			fmt.Println("Available roles:")
			for i, r := range roles {
				fmt.Printf("  [%d] %s\n", i+1, r.Name)
			}
			fmt.Print("Choose (number): ")
			line, _ := reader.ReadString('\n')
			idx, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr != nil || idx < 1 || idx > len(roles) {
				return fmt.Errorf("invalid choice")
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
	fmt.Printf("Saved: [profile %s] in ~/.aws/config, token cache in ~/.aws/sso/cache\n", *profileName)
	fmt.Println(`Add this provider to config.json:`)
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
