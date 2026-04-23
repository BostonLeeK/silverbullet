package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/render"
)

// path to auth page in the client bundle
const authPagePath = ".client/auth.html"

// excludedPrefixes: paths starting with these strings bypass auth.
var excludedPrefixes = []string{
	"/.client/", // let the basic UI assets load before login
}

// excludedExact: these paths bypass auth only when matched exactly.
// The admin endpoints under /.auth/* (users, acls, me) must stay protected,
// so we can't prefix-match /.auth/ — only the login/setup endpoints are public.
var excludedExact = map[string]bool{
	"/service_worker.js": true, // fetched without cookies by the browser
	"/.auth":             true, // login page / POST
	"/.auth/setup":       true, // first-run admin creation
	"/.logout":           true,
	"/.ping":             true,
}

type userContextKey struct{}

func userFromContext(ctx context.Context) *User {
	if v := ctx.Value(userContextKey{}); v != nil {
		if u, ok := v.(*User); ok {
			return u
		}
	}
	return nil
}

func requireUser(w http.ResponseWriter, r *http.Request) *User {
	u := userFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	return u
}

func requireAdmin(w http.ResponseWriter, r *http.Request) *User {
	u := requireUser(w, r)
	if u == nil {
		return nil
	}
	if u.Role != RoleAdmin {
		http.Error(w, "Forbidden: admin only", http.StatusForbidden)
		return nil
	}
	return u
}

func addAuthEndpoints(r chi.Router, config *ServerConfig) {
	// Logout
	r.Get("/.logout", func(w http.ResponseWriter, r *http.Request) {
		host := extractHost(r)
		cookieOptions := CookieOptions{Path: fmt.Sprintf("%s/", config.HostURLPrefix)}
		deleteCookie(w, authCookieName(host), cookieOptions)
		deleteCookie(w, "refreshLogin", cookieOptions)
		http.Redirect(w, r, applyURLPrefix("/.auth", config.HostURLPrefix), http.StatusFound)
	})

	// Auth page (GET)
	r.Get("/.auth", func(w http.ResponseWriter, r *http.Request) {
		spaceConfig := spaceConfigFromContext(r.Context())
		if spaceConfig.Auth == nil || spaceConfig.UserStore == nil {
			http.Error(w, "Authentication not enabled", http.StatusForbidden)
			return
		}
		if err := spaceConfig.InitAuth(); err != nil {
			http.Error(w, "Failed to initialize authentication", http.StatusInternalServerError)
			return
		}

		hasUsers, err := spaceConfig.UserStore.HasAnyUser()
		if err != nil {
			http.Error(w, "Auth database error", http.StatusInternalServerError)
			return
		}

		data, _, err := config.ClientBundle.ReadFile(authPagePath)
		if err != nil {
			http.Error(w, "Auth page not found", http.StatusNotFound)
			return
		}

		tpl := template.Must(template.New("auth").Parse(string(data)))

		templateData := map[string]any{
			"HostPrefix":     config.HostURLPrefix,
			"SpaceName":      spaceConfig.SpaceName,
			"EncryptionSalt": spaceConfig.JwtIssuer.Salt,
			"RememberMeDays": spaceConfig.Auth.RememberMeHours / 24,
			"SetupMode":      !hasUsers,
		}

		w.Header().Set("Content-type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := tpl.Execute(w, templateData); err != nil {
			log.Printf("Could not render auth page: %v", err)
			w.Write([]byte("Server error"))
		}
	})

	// First-run setup: create admin if no users exist
	r.Post("/.auth/setup", func(w http.ResponseWriter, r *http.Request) {
		spaceConfig := spaceConfigFromContext(r.Context())
		if spaceConfig.UserStore == nil {
			http.Error(w, "Auth not enabled", http.StatusForbidden)
			return
		}
		hasUsers, err := spaceConfig.UserStore.HasAnyUser()
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}
		if hasUsers {
			render.JSON(w, r, map[string]any{"status": "error", "error": "Setup already completed"})
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		email := r.FormValue("email")
		password := r.FormValue("password")
		if email == "" || password == "" {
			render.JSON(w, r, map[string]any{"status": "error", "error": "Email and password required"})
			return
		}
		if len(password) < 8 {
			render.JSON(w, r, map[string]any{"status": "error", "error": "Password must be at least 8 characters"})
			return
		}
		if err := spaceConfig.InitAuth(); err != nil {
			http.Error(w, "Auth init failed", http.StatusInternalServerError)
			return
		}
		user, err := spaceConfig.UserStore.CreateUser(email, password, RoleAdmin)
		if err != nil {
			render.JSON(w, r, map[string]any{"status": "error", "error": err.Error()})
			return
		}
		log.Printf("Setup: created initial admin user %s (id=%d)", user.Email, user.ID)
		issueSessionCookie(w, r, config, spaceConfig, user, true)
		render.JSON(w, r, map[string]any{"status": "ok", "redirect": applyURLPrefix("/", config.HostURLPrefix)})
	})

	// Auth POST endpoint (login)
	r.Post("/.auth", func(w http.ResponseWriter, r *http.Request) {
		spaceConfig := spaceConfigFromContext(r.Context())
		if spaceConfig.UserStore == nil {
			http.Error(w, "Auth not enabled", http.StatusForbidden)
			return
		}
		if err := spaceConfig.InitAuth(); err != nil {
			http.Error(w, "Failed to initialize authentication", http.StatusInternalServerError)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		// Accept either "email" or legacy "username" form field
		email := r.FormValue("email")
		if email == "" {
			email = r.FormValue("username")
		}
		password := r.FormValue("password")
		rememberMe := r.FormValue("rememberMe")
		from := r.FormValue("from")

		if email == "" || password == "" {
			render.JSON(w, r, map[string]any{"status": "error", "error": "Email and password required"})
			return
		}

		if spaceConfig.LockoutTimer.IsLocked() {
			log.Println("Authentication locked out")
			render.JSON(w, r, map[string]any{"status": "error", "error": "Too many attempts, try again later"})
			return
		}

		user, err := spaceConfig.UserStore.VerifyPassword(email, password)
		if err != nil {
			spaceConfig.LockoutTimer.AddCount()
			render.JSON(w, r, map[string]any{"status": "error", "error": "Invalid email or password"})
			return
		}

		issueSessionCookie(w, r, config, spaceConfig, user, rememberMe != "")

		redirectPath := applyURLPrefix("/", config.HostURLPrefix)
		if from != "" {
			redirectPath = from
		}
		render.JSON(w, r, map[string]any{"status": "ok", "redirect": redirectPath})
	})

	// Info about the currently logged in user
	r.Get("/.auth/me", func(w http.ResponseWriter, r *http.Request) {
		u := requireUser(w, r)
		if u == nil {
			return
		}
		render.JSON(w, r, u)
	})

	// --- Admin: user management ---
	r.Get("/.auth/users", func(w http.ResponseWriter, r *http.Request) {
		if requireAdmin(w, r) == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		users, err := spaceConfig.UserStore.ListUsers()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render.JSON(w, r, users)
	})

	r.Post("/.auth/users", func(w http.ResponseWriter, r *http.Request) {
		if requireAdmin(w, r) == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		var body struct {
			Email    string `json:"email"`
			Password string `json:"password"`
			Role     string `json:"role"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Bad JSON", http.StatusBadRequest)
			return
		}
		if body.Role == "" {
			body.Role = RoleUser
		}
		if len(body.Password) < 8 {
			http.Error(w, "Password must be at least 8 characters", http.StatusBadRequest)
			return
		}
		u, err := spaceConfig.UserStore.CreateUser(body.Email, body.Password, body.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		render.JSON(w, r, u)
	})

	r.Patch("/.auth/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor := requireAdmin(w, r)
		if actor == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "Bad id", http.StatusBadRequest)
			return
		}
		var body struct {
			Password *string `json:"password,omitempty"`
			Role     *string `json:"role,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Bad JSON", http.StatusBadRequest)
			return
		}
		if body.Password != nil {
			if len(*body.Password) < 8 {
				http.Error(w, "Password must be at least 8 characters", http.StatusBadRequest)
				return
			}
			if err := spaceConfig.UserStore.UpdatePassword(id, *body.Password); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if body.Role != nil {
			// Guard: don't let admins demote themselves if they're the last admin
			if id == actor.ID && *body.Role != RoleAdmin {
				count, _ := spaceConfig.UserStore.CountAdmins()
				if count <= 1 {
					http.Error(w, "Cannot demote the last admin", http.StatusBadRequest)
					return
				}
			}
			if err := spaceConfig.UserStore.UpdateRole(id, *body.Role); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		u, err := spaceConfig.UserStore.GetUser(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		render.JSON(w, r, u)
	})

	r.Delete("/.auth/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor := requireAdmin(w, r)
		if actor == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "Bad id", http.StatusBadRequest)
			return
		}
		if id == actor.ID {
			http.Error(w, "Cannot delete your own account", http.StatusBadRequest)
			return
		}
		target, err := spaceConfig.UserStore.GetUser(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if target.Role == RoleAdmin {
			count, _ := spaceConfig.UserStore.CountAdmins()
			if count <= 1 {
				http.Error(w, "Cannot delete the last admin", http.StatusBadRequest)
				return
			}
		}
		if err := spaceConfig.UserStore.DeleteUser(id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// --- Admin: folder ACL management ---
	r.Get("/.auth/acls", func(w http.ResponseWriter, r *http.Request) {
		if requireAdmin(w, r) == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		acls, err := spaceConfig.UserStore.ListAllACLs()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		render.JSON(w, r, acls)
	})

	r.Post("/.auth/acls", func(w http.ResponseWriter, r *http.Request) {
		if requireAdmin(w, r) == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		var body struct {
			FolderPath string `json:"folderPath"`
			UserID     int64  `json:"userId"`
			Permission string `json:"permission"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Bad JSON", http.StatusBadRequest)
			return
		}
		if err := spaceConfig.UserStore.SetFolderACL(body.FolderPath, body.UserID, body.Permission); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Delete("/.auth/acls", func(w http.ResponseWriter, r *http.Request) {
		if requireAdmin(w, r) == nil {
			return
		}
		spaceConfig := spaceConfigFromContext(r.Context())
		folder := r.URL.Query().Get("folder")
		uid, err := strconv.ParseInt(r.URL.Query().Get("userId"), 10, 64)
		if err != nil {
			http.Error(w, "Bad userId", http.StatusBadRequest)
			return
		}
		if err := spaceConfig.UserStore.RemoveFolderACL(folder, uid); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// issueSessionCookie creates a JWT and sets the auth cookie.
func issueSessionCookie(w http.ResponseWriter, r *http.Request, config *ServerConfig, spaceConfig *SpaceConfig, user *User, rememberMe bool) {
	payload := map[string]any{
		"sub":   user.ID,
		"email": user.Email,
		"role":  user.Role,
	}
	expirySeconds := authenticationExpirySeconds
	if rememberMe {
		expirySeconds = spaceConfig.Auth.RememberMeHours * 60 * 60
	}
	jwt, err := spaceConfig.JwtIssuer.CreateJWT(payload, expirySeconds)
	if err != nil {
		log.Printf("Failed to create JWT: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	host := extractHost(r)
	expires := time.Now().Add(time.Duration(expirySeconds) * time.Second)
	cookieOptions := CookieOptions{
		Path:    fmt.Sprintf("%s/", config.HostURLPrefix),
		Expires: expires,
	}
	setCookie(w, authCookieName(host), jwt, cookieOptions)
	if rememberMe {
		setCookie(w, "refreshLogin", "true", cookieOptions)
	}
}

func (spaceConfig *SpaceConfig) InitAuth() error {
	if spaceConfig.JwtIssuer == nil {
		spaceConfig.authMutex.Lock()
		defer spaceConfig.authMutex.Unlock()

		if spaceConfig.JwtIssuer != nil {
			return nil
		}

		var err error
		spaceConfig.JwtIssuer, err = CreateAuthenticator(path.Join(spaceConfig.SpaceFolderPath, ".silverbullet.auth.json"), spaceConfig.Auth)
		if err != nil {
			return err
		}

		if spaceConfig.Auth.LockoutLimit > 0 {
			spaceConfig.LockoutTimer = NewLockoutTimer(spaceConfig.Auth.LockoutTime*1000, spaceConfig.Auth.LockoutLimit)
		} else {
			spaceConfig.LockoutTimer = NewLockoutTimer(0, 0)
		}
	}
	return nil
}

// authMiddleware provides authentication middleware
func authMiddleware(config *ServerConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			spaceConfig := spaceConfigFromContext(r.Context())
			if spaceConfig.Auth == nil {
				next.ServeHTTP(w, r)
				return
			}

			reqPath := removeURLPrefix(r.URL.Path, config.HostURLPrefix)
			host := extractHost(r)

			// Headless token authentication: issue a JWT cookie and proceed.
			if config.HeadlessToken != "" {
				if queryToken := r.URL.Query().Get("token"); queryToken == config.HeadlessToken {
					if err := spaceConfig.InitAuth(); err != nil {
						http.Error(w, "Failed to initialize authentication", http.StatusInternalServerError)
						return
					}
					jwt, err := spaceConfig.JwtIssuer.CreateJWT(map[string]any{
						"sub":   int64(0),
						"email": "headless",
						"role":  RoleAdmin,
					})
					if err != nil {
						log.Printf("[Headless] Failed to create JWT: %v", err)
						http.Error(w, "Internal server error", http.StatusInternalServerError)
						return
					}
					cookieOptions := CookieOptions{Path: fmt.Sprintf("%s/", config.HostURLPrefix)}
					setCookie(w, authCookieName(host), jwt, cookieOptions)
					// Attach synthetic admin user
					r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, &User{ID: 0, Email: "headless", Role: RoleAdmin}))
					next.ServeHTTP(w, r)
					return
				}
			}

			if isExcludedPath(reqPath) {
				next.ServeHTTP(w, r)
				return
			}

			if err := spaceConfig.InitAuth(); err != nil {
				http.Error(w, "Failed to initialize authentication", http.StatusInternalServerError)
				return
			}

			// Bearer token auth (uses a real user account via "email:password" or similar
			// is not supported; the Bearer path here keeps the old shared-token behaviour
			// for CI-style access). Tokens grant admin-equivalent access.
			authHeader := r.Header.Get("Authorization")
			if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok && spaceConfig.Auth.AuthToken != "" {
				if after != spaceConfig.Auth.AuthToken {
					log.Println("Unauthorized bearer token")
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, &User{ID: 0, Email: "token", Role: RoleAdmin}))
				next.ServeHTTP(w, r)
				return
			}

			authCookie := getCookie(r, authCookieName(host))
			if authCookie == "" {
				log.Printf("Unauthorized access to %s, redirecting to auth page", reqPath)
				redirectToAuth(w, "/.auth", reqPath, config.HostURLPrefix)
				return
			}

			claims, err := spaceConfig.JwtIssuer.VerifyAndDecodeJWT(authCookie)
			if err != nil {
				log.Printf("Error verifying JWT on %s: %v", reqPath, err)
				redirectToAuth(w, "/.auth", reqPath, config.HostURLPrefix)
				return
			}

			subRaw, ok := claims["sub"]
			if !ok {
				redirectToAuth(w, "/.auth", reqPath, config.HostURLPrefix)
				return
			}
			var userID int64
			switch v := subRaw.(type) {
			case float64:
				userID = int64(v)
			case int64:
				userID = v
			}

			var user *User
			if userID > 0 {
				user, err = spaceConfig.UserStore.GetUser(userID)
				if err != nil {
					log.Printf("JWT refers to missing user id=%d, forcing re-login", userID)
					redirectToAuth(w, "/.auth", reqPath, config.HostURLPrefix)
					return
				}
			} else if email, _ := claims["email"].(string); email == "headless" {
				user = &User{ID: 0, Email: "headless", Role: RoleAdmin}
			} else {
				redirectToAuth(w, "/.auth", reqPath, config.HostURLPrefix)
				return
			}

			refreshLogin(w, r, config, host, spaceConfig, user)
			r = r.WithContext(context.WithValue(r.Context(), userContextKey{}, user))
			next.ServeHTTP(w, r)
		})
	}
}

// refreshLogin refreshes the login cookie if the refreshLogin marker is set.
func refreshLogin(w http.ResponseWriter, r *http.Request, config *ServerConfig, host string, spaceConfig *SpaceConfig, user *User) {
	if getCookie(r, "refreshLogin") == "" {
		return
	}
	expirySeconds := spaceConfig.Auth.RememberMeHours * 60 * 60
	payload := map[string]any{
		"sub":   user.ID,
		"email": user.Email,
		"role":  user.Role,
	}
	newJwt, err := spaceConfig.JwtIssuer.CreateJWT(payload, expirySeconds)
	if err != nil {
		return
	}
	expires := time.Now().Add(time.Duration(expirySeconds) * time.Second)
	cookieOptions := CookieOptions{
		Path:    fmt.Sprintf("%s/", config.HostURLPrefix),
		Expires: expires,
	}
	setCookie(w, authCookieName(host), newJwt, cookieOptions)
	setCookie(w, "refreshLogin", "true", cookieOptions)
}

// LockoutTimer implements a simple rate limiter to prevent brute force attacks
type LockoutTimer struct {
	mutex       sync.Mutex
	bucketTime  int64
	bucketCount int
	bucketSize  int64 // duration in milliseconds
	limit       int
	disabled    bool
}

func NewLockoutTimer(countPeriodMs int, limit int) *LockoutTimer {
	disabled := math.IsNaN(float64(countPeriodMs)) || math.IsNaN(float64(limit)) ||
		countPeriodMs < 1 || limit < 1
	return &LockoutTimer{
		bucketSize: int64(countPeriodMs),
		limit:      limit,
		disabled:   disabled,
	}
}

func (lt *LockoutTimer) updateBucketTime() {
	currentBucketTime := time.Now().UnixMilli() / lt.bucketSize
	if lt.bucketTime == currentBucketTime {
		return
	}
	lt.bucketTime = currentBucketTime
	lt.bucketCount = 0
}

func (lt *LockoutTimer) IsLocked() bool {
	if lt.disabled {
		return false
	}
	lt.mutex.Lock()
	defer lt.mutex.Unlock()
	lt.updateBucketTime()
	return lt.bucketCount >= lt.limit
}

func (lt *LockoutTimer) AddCount() {
	if lt.disabled {
		return
	}
	lt.mutex.Lock()
	defer lt.mutex.Unlock()
	lt.bucketCount++
}
