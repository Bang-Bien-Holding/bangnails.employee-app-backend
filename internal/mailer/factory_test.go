package mailer

import "testing"

func TestNew(t *testing.T) {
	tests := []struct {
		name        string
		cfg         Config
		wantErr     bool
		wantMailpit bool
		wantBrevo   bool
	}{
		{
			name:        "development defaults to Mailpit",
			cfg:         Config{Env: EnvDevelopment, MailpitAddr: "localhost:1025"},
			wantMailpit: true,
		},
		{
			name:        "staging uses Mailpit",
			cfg:         Config{Env: EnvStaging, MailpitAddr: "localhost:1025"},
			wantMailpit: true,
		},
		{
			name:        "empty Env defaults to Mailpit",
			cfg:         Config{MailpitAddr: "localhost:1025"},
			wantMailpit: true,
		},
		{
			name:      "production uses Brevo when an API key is set",
			cfg:       Config{Env: EnvProduction, BrevoAPIKey: "test-key"},
			wantBrevo: true,
		},
		{
			name:    "production without an API key fails fast",
			cfg:     Config{Env: EnvProduction},
			wantErr: true,
		},
		{
			name:    "unknown Env fails fast",
			cfg:     Config{Env: "qa"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New(tc.cfg)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			if tc.wantMailpit {
				if _, ok := client.(*MailpitClient); !ok {
					t.Errorf("expected *MailpitClient, got %T", client)
				}
			}
			if tc.wantBrevo {
				if _, ok := client.(*BrevoClient); !ok {
					t.Errorf("expected *BrevoClient, got %T", client)
				}
			}
		})
	}
}
