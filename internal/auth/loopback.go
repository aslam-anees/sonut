package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"
)

// authCallbackPort is the fixed port for the OAuth/magic-link loopback server.
// It must match the redirect URL registered in the Supabase project settings
// (Supabase only honours redirect_to values on its allowlist; a random port is
// never on that list so Supabase would fall back to the site URL instead).
const authCallbackPort = 2748

// LoopbackServer starts a local HTTP server to capture the OAuth redirect.
// The listener is bound immediately at creation so the port is held open;
// the magic link email is sent with that URL before Wait() is called.
type LoopbackServer struct {
	port     int
	listener net.Listener
	tokenCh  chan *Session
}

// NewLoopbackServer binds the fixed auth callback port (2748) so the URL it
// returns matches the redirect URL registered in the Supabase project.
// Returns an error when the port is busy (e.g. sonut-proxy.ts is still running).
func NewLoopbackServer() (*LoopbackServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", authCallbackPort))
	if err != nil {
		return nil, fmt.Errorf("auth callback port %d is already in use — stop any other process using that port (e.g. sonut-proxy) and try again: %w", authCallbackPort, err)
	}
	return &LoopbackServer{
		port:     ln.Addr().(*net.TCPAddr).Port,
		listener: ln,
		tokenCh:  make(chan *Session, 1),
	}, nil
}

// Port returns the port the server is actually listening on.
func (s *LoopbackServer) Port() int { return s.port }

// CallbackURL returns the redirect URL sent to Supabase. It always uses the
// fixed auth callback port (2748) because that is what must be registered in
// the Supabase project's allowed redirect URLs list. If we bound a different
// port (fallback), the magic link will still land on 2748 via Supabase's site
// URL, which won't be intercepted — but using the fixed URL at least keeps the
// Supabase allowlist check happy in the common case.
func (s *LoopbackServer) CallbackURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", authCallbackPort)
}

// Wait starts serving on the already-bound listener and blocks until a session
// arrives or ctx is cancelled (max 10 minutes).
func (s *LoopbackServer) Wait(ctx context.Context) (*Session, error) {
	mux := http.NewServeMux()

	// "/" and "/callback" both serve the same extraction page.
	// Magic links redirect to "/" with the hash fragment.
	// Google OAuth uses "/callback".
	serveCallback := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, callbackHTML)
	}
	mux.HandleFunc("/", serveCallback)
	mux.HandleFunc("/callback", serveCallback)

	// JS on the page extracts tokens from the URL hash and POSTs here.
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		accessToken := r.FormValue("access_token")
		refreshToken := r.FormValue("refresh_token")
		expiresIn := r.FormValue("expires_in")
		userEmail := r.FormValue("email")
		userID := r.FormValue("sub")

		if accessToken == "" {
			http.Error(w, "no access_token", http.StatusBadRequest)
			return
		}

		exp := 3600
		fmt.Sscanf(expiresIn, "%d", &exp)

		sess := &Session{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			ExpiresIn:    exp,
			ExpiresAt:    time.Now().Add(time.Duration(exp) * time.Second),
			User: User{
				ID:    userID,
				Email: userEmail,
			},
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, successHTML)

		select {
		case s.tokenCh <- sess:
		default:
		}
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(s.listener) //nolint:errcheck
	defer srv.Close()

	select {
	case sess := <-s.tokenCh:
		return sess, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Minute):
		return nil, fmt.Errorf("authentication timed out")
	}
}

// OpenBrowser opens url in the default system browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// BuildSupabaseGoogleURL constructs the Supabase OAuth authorize URL.
func BuildSupabaseGoogleURL(supabaseURL, redirectTo string) string {
	return fmt.Sprintf(
		"%s/auth/v1/authorize?provider=google&redirect_to=%s",
		supabaseURL,
		redirectTo,
	)
}

const callbackHTML = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>@code Auth</title>
<style>body{font-family:system-ui;text-align:center;padding:60px;background:#0a0c12;color:#e8eaf4}</style>
</head>
<body>
<h2 style="color:#4d9fff">Completing sign-in…</h2>
<p>Please wait while we finish authentication.</p>
<script>
(function(){
  var h = window.location.hash.substring(1);
  var params = {};
  h.split('&').forEach(function(p){var kv=p.split('=');params[decodeURIComponent(kv[0])]=decodeURIComponent(kv[1]||'');});
  if(!params.access_token){
    var q = window.location.search.substring(1);
    q.split('&').forEach(function(p){var kv=p.split('=');params[decodeURIComponent(kv[0])]=decodeURIComponent(kv[1]||'');});
  }
  if(!params.access_token){
    document.querySelector('p').textContent = 'No token received. Please request a new magic link.';
    return;
  }
  var payload = {};
  try{
    var parts = params.access_token.split('.');
    payload = JSON.parse(atob(parts[1].replace(/-/g,'+').replace(/_/g,'/')));
  }catch(e){}
  var form = document.createElement('form');
  form.method = 'POST';
  form.action = '/token';
  var fields = {
    access_token: params.access_token,
    refresh_token: params.refresh_token||'',
    expires_in: params.expires_in||'3600',
    email: payload.email||'',
    sub: payload.sub||''
  };
  Object.keys(fields).forEach(function(k){
    var i = document.createElement('input');
    i.type='hidden'; i.name=k; i.value=fields[k];
    form.appendChild(i);
  });
  document.body.appendChild(form);
  form.submit();
})();
</script>
</body></html>`

const successHTML = `<!DOCTYPE html>
<html>
<head><meta charset="utf-8"><title>@code — Signed in</title>
<style>body{font-family:system-ui;text-align:center;padding:60px;background:#0a0c12;color:#e8eaf4}</style>
</head>
<body>
<h2 style="color:#4d9fff">&#x2713; Signed in successfully</h2>
<p>You can close this tab and return to the terminal.</p>
</body></html>`
