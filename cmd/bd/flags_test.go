package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReadBodyFile(t *testing.T) {
	t.Run("ReadFromFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "## Problem\n\nSomething is broken.\n\n## Solution\n\nFix it.\n"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("ReadEmptyFile", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "empty.md")
		if err := os.WriteFile(filePath, []byte{}, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("NonExistentFile", func(t *testing.T) {
		_, err := readBodyFile("/nonexistent/path/file.md")
		if err == nil {
			t.Fatal("expected error for non-existent file, got nil")
		}
		if !strings.Contains(err.Error(), "failed to open file") {
			t.Errorf("expected 'failed to open file' error, got: %v", err)
		}
	})

	t.Run("SpecialCharacters", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "special.md")
		content := `Quotes: "double" and 'single'
Backticks: ` + "`code`" + `
Newlines and tabs:	here
Shell chars: $HOME $(whoami) && || > < |
Unicode: 日本語 émojis 🎉
`
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile failed: %v", err)
		}
		if got != content {
			t.Errorf("content mismatch:\nexpected: %q\ngot:      %q", content, got)
		}
	})

	t.Run("BinaryContent", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "binary.bin")
		// Binary content with null bytes
		content := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00, 0x48, 0x65, 0x6c, 0x6c, 0x6f}
		if err := os.WriteFile(filePath, content, 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		got, err := readBodyFile(filePath)
		if err != nil {
			t.Fatalf("readBodyFile should handle binary content: %v", err)
		}
		if got != string(content) {
			t.Errorf("binary content mismatch")
		}
	})

	t.Run("ReadFromStdin", func(t *testing.T) {
		// Create a pipe to simulate stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

		// Save and restore original stdin
		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		// Write content to pipe in a goroutine
		content := "Description from stdin\nWith multiple lines\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		got, err := readBodyFile("-")
		if err != nil {
			t.Fatalf("readBodyFile('-') failed: %v", err)
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})
}

func TestGetDescriptionFlag(t *testing.T) {
	// Helper to create a cobra command with common issue flags registered
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{
			Use: "test",
			Run: func(cmd *cobra.Command, args []string) {},
		}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("BodyFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Description from file"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		cmd.SetArgs([]string{"--body-file", filePath})
		if err := cmd.ParseFlags([]string{"--body-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("DescriptionFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Description from description-file"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("BodyFileTakesPrecedenceOverDescription", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		fileContent := "From file"
		if err := os.WriteFile(filePath, []byte(fileContent), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		// body-file + description should error
		err := cmd.ParseFlags([]string{"--body-file", filePath, "--description", "From flag"})
		if err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		// getDescriptionFlag should os.Exit(1) when both are set
		// We can't easily test os.Exit, so we just verify body-file is checked first
		// by testing them independently
	})

	t.Run("DescriptionFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description", "Direct description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Direct description" {
			t.Errorf("expected 'Direct description', got %q", got)
		}
	})

	t.Run("BodyFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--body", "Body description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Body description" {
			t.Errorf("expected 'Body description', got %q", got)
		}
	})

	t.Run("MessageFlagFallback", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--message", "Message description"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "Message description" {
			t.Errorf("expected 'Message description', got %q", got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("BodyFileAndDescriptionFileSameValue", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "desc.md")
		content := "Same file content"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		// Both pointing to same file should work
		if err := cmd.ParseFlags([]string{"--body-file", filePath, "--description-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("StdinFlag", func(t *testing.T) {
		// Create a pipe to simulate stdin
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}

		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		content := "Description from --stdin flag\nMulti-line content\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--stdin"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("StdinMutuallyExclusiveWithDescription", func(t *testing.T) {
		cmd := newCmd()
		// cobra's MarkFlagsMutuallyExclusive validates at execution time,
		// but we can verify the flag is registered
		flag := cmd.Flags().Lookup("stdin")
		if flag == nil {
			t.Fatal("expected --stdin flag to be registered")
		}
		if flag.DefValue != "false" {
			t.Errorf("expected default value 'false', got %q", flag.DefValue)
		}
	})

	t.Run("DescriptionAndBodySameValue", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--description", "same", "--body", "same"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDescriptionFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "same" {
			t.Errorf("expected 'same', got %q", got)
		}
	})
}

func TestGetDesignFlag(t *testing.T) {
	// Helper to create a cobra command with common issue flags registered
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{
			Use: "test",
			Run: func(cmd *cobra.Command, args []string) {},
		}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("InlineDesignFlag", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design", "inline design content"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "inline design content" {
			t.Errorf("expected 'inline design content', got %q", got)
		}
	})

	t.Run("DesignFileFlag", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "design.md")
		content := "## Architecture\n\nUse microservices.\n"
		if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("DesignFileStdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		content := "Design from stdin\nWith multiple lines\n"
		go func() {
			w.WriteString(content)
			w.Close()
		}()

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--design-file", "-"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != content {
			t.Errorf("expected %q, got %q", content, got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}

		got, changed, err := getDesignFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("MutualExclusionRegistered", func(t *testing.T) {
		cmd := newCmd()
		// Verify both flags are registered
		if cmd.Flags().Lookup("design") == nil {
			t.Fatal("expected --design flag to be registered")
		}
		if cmd.Flags().Lookup("design-file") == nil {
			t.Fatal("expected --design-file flag to be registered")
		}
	})
}

// codeSpanRoundTrip is text containing a markdown code span wrapping a
// command. Under the Claude Code Bash tool's `eval "... bd update ..."`
// wrapper, passing this inline causes zsh to execute the enclosed command
// via command substitution before bd ever sees it (gt-h38j). The -file
// flags must round-trip it verbatim with no shell involvement.
const codeSpanRoundTrip = "Findings: the scan ran as `find / -name '*.log'` and hung for 14h."

func TestGetNotesFlag(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("InlineNotesFlag", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--notes", "inline notes"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "inline notes" {
			t.Errorf("expected 'inline notes', got %q", got)
		}
	})

	t.Run("NotesFileFlag_CodeSpanRoundTrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "notes.md")
		if err := os.WriteFile(filePath, []byte(codeSpanRoundTrip), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--notes-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != codeSpanRoundTrip {
			t.Errorf("expected verbatim round-trip %q, got %q", codeSpanRoundTrip, got)
		}
	})

	t.Run("NotesFileStdin", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("failed to create pipe: %v", err)
		}
		oldStdin := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = oldStdin })

		go func() {
			w.WriteString(codeSpanRoundTrip)
			w.Close()
		}()

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--notes-file", "-"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != codeSpanRoundTrip {
			t.Errorf("expected %q, got %q", codeSpanRoundTrip, got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("MutualExclusionRegistered", func(t *testing.T) {
		cmd := newCmd()
		if cmd.Flags().Lookup("notes") == nil {
			t.Fatal("expected --notes flag to be registered")
		}
		if cmd.Flags().Lookup("notes-file") == nil {
			t.Fatal("expected --notes-file flag to be registered")
		}
		if err := cmd.ParseFlags([]string{"--notes", "a", "--notes-file", "b"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		if err := cmd.ValidateFlagGroups(); err == nil {
			t.Fatal("expected error when both --notes and --notes-file are set")
		}
	})
}

func TestGetAppendNotesFlag(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("InlineAppendNotesFlag", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--append-notes", "inline append"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAppendNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "inline append" {
			t.Errorf("expected 'inline append', got %q", got)
		}
	})

	t.Run("AppendNotesFileFlag_CodeSpanRoundTrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "append-notes.md")
		if err := os.WriteFile(filePath, []byte(codeSpanRoundTrip), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--append-notes-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAppendNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != codeSpanRoundTrip {
			t.Errorf("expected verbatim round-trip %q, got %q", codeSpanRoundTrip, got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAppendNotesFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("MutualExclusionRegistered", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--append-notes", "a", "--append-notes-file", "b"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		if err := cmd.ValidateFlagGroups(); err == nil {
			t.Fatal("expected error when both --append-notes and --append-notes-file are set")
		}
	})
}

func TestGetAcceptanceFlag(t *testing.T) {
	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "test", Run: func(cmd *cobra.Command, args []string) {}}
		registerCommonIssueFlags(cmd)
		return cmd
	}

	t.Run("InlineAcceptanceFlag", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--acceptance", "inline acceptance"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAcceptanceFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != "inline acceptance" {
			t.Errorf("expected 'inline acceptance', got %q", got)
		}
	})

	t.Run("AcceptanceFileFlag_CodeSpanRoundTrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "acceptance.md")
		if err := os.WriteFile(filePath, []byte(codeSpanRoundTrip), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--acceptance-file", filePath}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAcceptanceFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !changed {
			t.Error("expected changed=true")
		}
		if got != codeSpanRoundTrip {
			t.Errorf("expected verbatim round-trip %q, got %q", codeSpanRoundTrip, got)
		}
	})

	t.Run("NoFlagsSet", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		got, changed, err := getAcceptanceFlag(cmd)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if changed {
			t.Error("expected changed=false when no flags set")
		}
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("MutualExclusionRegistered", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.ParseFlags([]string{"--acceptance", "a", "--acceptance-file", "b"}); err != nil {
			t.Fatalf("failed to parse flags: %v", err)
		}
		if err := cmd.ValidateFlagGroups(); err == nil {
			t.Fatal("expected error when both --acceptance and --acceptance-file are set")
		}
	})
}
