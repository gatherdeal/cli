package cli

import (
	"testing"
)

func TestRevokeErrorBodyOn200(t *testing.T) {
	for _, body := range []string{`{"error":"invalid_client"}`, `{"status":"error","error":{"code":"X","message":"y"}}`, `<html>ok</html>`} {
		t.Run(body, func(t *testing.T) {
			fake, te := loggedIn(t)
			fake.revokeBody = body
			res := te.run("auth", "logout")
			if res.exit == 0 || res.errorCode() != "CLI_REVOKE_FAILED" {
				t.Fatalf("exit %d code %s", res.exit, res.errorCode())
			}
			details := res.env["error"].(map[string]any)["details"].(map[string]any)
			if details["credential_deleted"] != true || len(te.keyring.items) != 0 {
				t.Fatal("local credential not deleted")
			}
		})
	}
	// An empty 200 body is success.
	fake, te := loggedIn(t)
	fake.revokeBody = " "
	if res := te.run("auth", "logout"); res.exit != 0 || res.data()["revoked"] != true {
		t.Fatalf("empty body: %s", res.stdout)
	}
}
