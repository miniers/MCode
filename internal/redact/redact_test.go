package redact

import (
	"strings"
	"testing"
)

func TestSecretsNewFormats(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		want        string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name: "PEM private key multiline",
			input: "prefix text\n" +
				"-----BEGIN RSA PRIVATE KEY-----\n" +
				"AAAA1111BBBB2222CCCC3333DDDD4444\n" +
				"EEEE5555FFFF6666GGGG7777HHHH8888\n" +
				"-----END RSA PRIVATE KEY-----\n" +
				"suffix text",
			want: "prefix text\n" +
				"-----BEGIN PRIVATE KEY-----***-----END PRIVATE KEY-----\n" +
				"suffix text",
			wantContain: []string{
				"prefix text\n",
				"-----BEGIN PRIVATE KEY-----***-----END PRIVATE KEY-----",
				"\nsuffix text",
			},
			wantAbsent: []string{
				"AAAA1111BBBB2222CCCC3333DDDD4444",
				"EEEE5555FFFF6666GGGG7777HHHH8888",
			},
		},
		{
			name:        "AWS access key ID",
			input:       "aws_id=AKIA1234567890ABCDEF",
			want:        "aws_id=AKIA***",
			wantContain: []string{"AKIA***"},
			wantAbsent:  []string{"1234567890ABCDEF"},
		},
		{
			name:        "GitLab personal access token",
			input:       "gl=glpat-abcde12345fghij67890",
			want:        "gl=glpat-***",
			wantContain: []string{"glpat-***"},
			wantAbsent:  []string{"abcde12345fghij67890"},
		},
		{
			name:        "Hugging Face token",
			input:       "hf=hf_1234567890abcdefghij1234567890",
			want:        "hf=hf_***",
			wantContain: []string{"hf_***"},
			wantAbsent:  []string{"1234567890abcdefghij1234567890"},
		},
		{
			name:  "plain text unchanged",
			input: "hello world hf_short",
			want:  "hello world hf_short",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Secrets(tt.input)
			if got != tt.want {
				t.Fatalf("Secrets(%q) = %q, want %q", tt.input, got, tt.want)
			}
			for _, sub := range tt.wantContain {
				if !strings.Contains(got, sub) {
					t.Errorf("Secrets(%q) = %q, missing %q", tt.input, got, sub)
				}
			}
			for _, sub := range tt.wantAbsent {
				if strings.Contains(got, sub) {
					t.Errorf("Secrets(%q) = %q, still contains secret %q", tt.input, got, sub)
				}
			}
		})
	}
}
