// scopecheck проверяет, какие Google-скоупы реально делегированы сервис-аккаунту
// (domain-wide delegation): по каждому скоупу отдельно запрашивает токен с
// impersonation и печатает OK/FAIL. Список скоупов в JSON-ключе не хранится,
// поэтому проверить выдачу можно только так.
package main

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/oauth2/google"
)

var scopes = []struct{ owner, scope string }{
	{"gdocs", "https://www.googleapis.com/auth/drive.activity.readonly"},
	{"gdocs", "https://www.googleapis.com/auth/drive.readonly"},
	{"gdocs", "https://www.googleapis.com/auth/drive.metadata.readonly"},
	{"gcal", "https://www.googleapis.com/auth/calendar.readonly"},
	{"gwork", "https://www.googleapis.com/auth/admin.reports.audit.readonly"},
	{"gwork", "https://www.googleapis.com/auth/admin.reports.usage.readonly"},
}

func main() {
	keyFile := os.Args[1]
	subject := os.Args[2]

	raw, err := os.ReadFile(keyFile)
	if err != nil {
		fmt.Println("read key:", err)
		os.Exit(1)
	}

	ctx := context.Background()
	failed := 0
	for _, s := range scopes {
		cfg, err := google.JWTConfigFromJSON(raw, s.scope)
		if err != nil {
			fmt.Println("parse key:", err)
			os.Exit(1)
		}
		cfg.Subject = subject
		_, err = cfg.TokenSource(ctx).Token()
		if err != nil {
			failed++
			fmt.Printf("FAIL  [%s] %s\n      %v\n", s.owner, s.scope, err)
		} else {
			fmt.Printf("OK    [%s] %s\n", s.owner, s.scope)
		}
	}
	if failed > 0 {
		os.Exit(2)
	}
}
