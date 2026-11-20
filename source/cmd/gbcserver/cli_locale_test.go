package main

import (
	"strings"
	"testing"
)

func TestDetectCLILanguage(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{name: "Chinese locale", input: []string{"zh_CN.UTF-8"}, want: "zh-CN"},
		{name: "English locale", input: []string{"en_US.UTF-8"}, want: "en"},
		{name: "unknown locale falls back to Chinese", input: []string{"fr_FR.UTF-8"}, want: "zh-CN"},
		{name: "unknown high priority locale wins", input: []string{"fr_FR.UTF-8", "en_US.UTF-8"}, want: "zh-CN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runCLILanguageDetectionForTest(test.input...); got != test.want {
				t.Fatalf("detectCLILanguage(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestSelectCLILanguageUsesSystemEnvironmentAndChineseFallback(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "en_GB.UTF-8")
	if _, err := selectCLILanguage(nil); err != nil {
		t.Fatal(err)
	}
	if activeCLILanguage != cliLanguageEnglish {
		t.Fatalf("LANG should select English, got %q", activeCLILanguage)
	}

	t.Setenv("LANG", "fr_FR.UTF-8")
	if _, err := selectCLILanguage(nil); err != nil {
		t.Fatal(err)
	}
	if activeCLILanguage != cliLanguageChinese {
		t.Fatalf("unknown system locale should fall back to Chinese, got %q", activeCLILanguage)
	}
}

func TestSelectCLILanguageOnlyConsumesLeadingGlobalOption(t *testing.T) {
	args, err := selectCLILanguage([]string{"--lang", "en", "start", "--lang=zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if activeCLILanguage != cliLanguageEnglish || strings.Join(args, " ") != "start --lang=zh-CN" {
		t.Fatalf("language=%q args=%q", activeCLILanguage, args)
	}

	args, err = selectCLILanguage([]string{"start", "--lang=en"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[1] != "--lang=en" {
		t.Fatalf("command arguments were consumed: %q", args)
	}
}

func TestUnknownExplicitCLILanguageReturnsBilingualError(t *testing.T) {
	_, err := selectCLILanguage([]string{"--lang=fr", "--help"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported language \"fr\"") || !strings.Contains(err.Error(), "不支持的语言 \"fr\"") {
		t.Fatalf("expected bilingual unsupported-language error, got %v", err)
	}
	_, err = selectCLILanguage([]string{"--lang", "en", "--lang", "fr"})
	if err == nil || !strings.Contains(err.Error(), "Unsupported language") || !strings.Contains(err.Error(), "不支持的语言") {
		t.Fatalf("expected bilingual error after language selection, got %v", err)
	}
}

func TestEnglishHelpAndCommandOptionDescriptions(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	code, output := captureCommand(t, []string{"--lang", "en", "start", "--help"})
	if code != 0 || !strings.Contains(output, "Usage: gbcserver start") || !strings.Contains(output, "Run in the background on Linux") {
		t.Fatalf("English help was not localized, code=%d output=%s", code, output)
	}
}

func TestEnglishRootHelpIncludesGlobalLanguageSyntax(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	code, output := captureCommand(t, []string{"--lang=en", "--help"})
	if code != 0 || !strings.Contains(output, "Global language option before the command: --lang zh-CN|en") || !strings.Contains(output, "system language is used") {
		t.Fatalf("root help is missing global language syntax, code=%d output=%s", code, output)
	}
}

func TestEnglishCommonCommandError(t *testing.T) {
	code, output := captureCommand(t, []string{"--lang=en", "init-db"})
	if code != 2 || !strings.Contains(output, "--db is required") || strings.Contains(output, "必须提供 --db") {
		t.Fatalf("command error was not localized, code=%d output=%s", code, output)
	}
}

func TestEnglishCLIParseErrorsStayLocalized(t *testing.T) {
	code, output := captureCommand(t, []string{"--lang=en", "admin-path"})
	if code != 2 || !strings.Contains(output, "Provide one configuration value; use: admin-path <value>") || strings.Contains(output, "请使用") {
		t.Fatalf("missing-value error was not fully localized, code=%d output=%s", code, output)
	}

	code, output = captureCommand(t, []string{"--lang=zh-CN", "start", "--bogus"})
	if code != 2 || !strings.Contains(output, "选项无效；请使用 --help 查看用法") || strings.Contains(output, "flag provided") {
		t.Fatalf("unknown-option error was not localized, code=%d output=%s", code, output)
	}
}

func TestEnglishInteractivePluginTokenPrompts(t *testing.T) {
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	t.Setenv("LANG", "")
	if _, err := selectCLILanguage([]string{"--lang=en"}); err != nil {
		t.Fatal(err)
	}
	if got := cliText("为 %s 签发 %s 令牌？[y/N]："); got != "Issue a token for %s with scope %s? [y/N]: " {
		t.Fatalf("confirmation prompt translation = %q", got)
	}
	if got := cliText("请选择："); got != "Choose an option: " {
		t.Fatalf("menu prompt translation = %q", got)
	}
	if got := cliText("已撤销"); got != "Revoked" {
		t.Fatalf("token state translation = %q", got)
	}
}
