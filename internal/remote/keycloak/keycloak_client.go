package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"log"
	"spaghetti/internal/user"
)

type roleRep struct {
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Attributes map[string][]string `json:"attributes"`
}

// cache per roles-by-id (riduce chiamate)
type roleCache struct {
	mu   sync.RWMutex
	byID map[string]*roleRep
}

type KeycloakConfig struct {
	BaseURL      string // es: https://keycloak.example.com
	Realm        string // es: myrealm
	ClientID     string // service account abilitato
	ClientSecret string
	Timeout      time.Duration // es: 10 * time.Second

	// Opzioni di lookup
	// Se true, cerca l'utente anche per attributo "walletAddress"
	EnableWalletAttributeLookup bool
	WalletAttributeName         string // default: "walletAddress"
}

type KeycloakClient struct {
	cfg   KeycloakConfig
	http  *http.Client
	mu    sync.Mutex
	token string
	exp   time.Time
}

// Costruttore
func NewKeycloakClient(cfg KeycloakConfig) *KeycloakClient {
	if cfg.WalletAttributeName == "" {
		cfg.WalletAttributeName = "walletAddress"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &KeycloakClient{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

func (kc *KeycloakClient) getUserRealmRoles(ctx context.Context, token, userID string) ([]roleRep, error) {
	endpoint := fmt.Sprintf("%s/admin/realms/%s/users/%s/role-mappings/realm",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm, userID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := kc.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getUserRealmRoles http %d: %s", resp.StatusCode, string(b))
	}
	var roles []roleRep
	if err := json.NewDecoder(resp.Body).Decode(&roles); err != nil {
		return nil, err
	}
	log.Default().Printf("[Keycloak] --> user %s has %d realm roles", userID, len(roles))
	return roles, nil
}

var globalRoleCache = &roleCache{byID: map[string]*roleRep{}}

func (kc *KeycloakClient) getRoleByID(ctx context.Context, token, roleID string) (*roleRep, error) {
	// cache read
	globalRoleCache.mu.RLock()
	if r := globalRoleCache.byID[roleID]; r != nil {
		globalRoleCache.mu.RUnlock()
		return r, nil
	}
	globalRoleCache.mu.RUnlock()

	endpoint := fmt.Sprintf("%s/admin/realms/%s/roles-by-id/%s",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm, roleID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := kc.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("getRoleByID http %d: %s", resp.StatusCode, string(b))
	}
	var r roleRep
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	if r.Attributes == nil {
		r.Attributes = map[string][]string{}
	}
	// cache write
	globalRoleCache.mu.Lock()
	globalRoleCache.byID[roleID] = &r
	globalRoleCache.mu.Unlock()
	log.Default().Printf("[Keycloak] --> loaded role %s (%s) with %d attributes", r.Name, r.ID, len(r.Attributes))
	return &r, nil
}

func (kc *KeycloakClient) collectRoleAttributes(ctx context.Context, token, userID string) (roleNames []string, perms map[string]bool, crud map[string]bool, err error) {
	perms = map[string]bool{}
	crud = map[string]bool{}

	roles, err := kc.getUserRealmRoles(ctx, token, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	roleNames = make([]string, 0, len(roles))

	asBool := func(vals []string) bool {
		if len(vals) == 0 {
			return false
		}
		v := strings.ToLower(strings.TrimSpace(vals[0]))
		return v == "true" || v == "1" || v == "yes"
	}

	for _, rm := range roles {
		roleNames = append(roleNames, rm.Name)

		// carica definizione del ruolo per leggerne gli ATTRIBUTI
		def, err := kc.getRoleByID(ctx, token, rm.ID)
		if err != nil {
			return nil, nil, nil, err
		}

		for k, vs := range def.Attributes {
			// supply.* → permesso di dominio
			if strings.HasPrefix(k, "supply.") {
				if asBool(vs) {
					perms[k] = true
				} // OR
			}
			// opzionale: abilita anche CRUD dai ruoli
			switch k {
			case "canCreate", "canRead", "canUpdate", "canDelete":
				if asBool(vs) {
					crud[k] = true
				}
			}
		}
		log.Printf("[Keycloak] --> role %s attributes: %+v", def.Name, def.Attributes)
	}
	log.Default().Printf("[Keycloak] --> user %s roles: %v", userID, roleNames)
	return roleNames, perms, crud, nil
}

// ------------------ implementazione dell'interfaccia ------------------

func (kc *KeycloakClient) FetchAttributes(ctx context.Context, address string) (*user.Attributes, error) {
	addr := strings.ToLower(strings.TrimSpace(address))
	if addr == "" {
		return nil, errors.New("address mancante")
	}

	tok, err := kc.ensureToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}

	u, err := kc.findUserByExactUsername(ctx, tok, addr)
	if err != nil {
		return nil, err
	}
	if u == nil && kc.cfg.EnableWalletAttributeLookup {
		u, err = kc.findUserByAttribute(ctx, tok, kc.cfg.WalletAttributeName, addr) // FIX
		if err != nil {
			return nil, err
		}
	}
	if u == nil {
		return &user.Attributes{CanCreate: false, CanRead: false, CanUpdate: false, CanDelete: false}, nil
	}

	// 1) Attributi a livello UTENTE
	out := mapUserAttrsToCRUD(u.Attributes)
	if out.Perms == nil {
		out.Perms = map[string]bool{}
	}

	// 2) Ruoli → roleNames + permessi supply.* + eventuali CRUD dai ruoli
	roleNames, rolePerms, roleCRUD, err := kc.collectRoleAttributes(ctx, tok, u.ID)
	if err != nil {
		return nil, err
	}

	// merge permessi supply.*
	for k, v := range rolePerms {
		if v {
			out.Perms[k] = true
		}
	}
	// merge CRUD (OR)
	if roleCRUD["canCreate"] {
		out.CanCreate = true
	}
	if roleCRUD["canRead"] {
		out.CanRead = true
	}
	if roleCRUD["canUpdate"] {
		out.CanUpdate = true
	}
	if roleCRUD["canDelete"] {
		out.CanDelete = true
	}
	log.Default().Printf("[Keycloak] --> user %s roles: %v", address, roleNames)
	out.Roles = roleNames
	return out, nil
}

func (kc *KeycloakClient) FetchAllUsers(ctx context.Context) ([]*user.User, error) {
	tok, err := kc.ensureToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}

	start, max := 0, 100
	var out []*user.User

	for {
		users, err := kc.listUsers(ctx, tok, start, max)
		if err != nil {
			return nil, err
		}
		if len(users) == 0 {
			break
		}

		for _, ku := range users {
			address := strings.ToLower(strings.TrimSpace(ku.Username))
			if kc.cfg.EnableWalletAttributeLookup {
				if val := first(ku.Attributes[kc.cfg.WalletAttributeName]); val != "" {
					address = strings.ToLower(val)
				}
			}
			attrs := mapUserAttrsToCRUD(ku.Attributes)
			if attrs.Perms == nil {
				attrs.Perms = map[string]bool{}
			}

			// arricchisci con RUOLI (nome + attributi ruolo)
			roleNames, rolePerms, roleCRUD, err := kc.collectRoleAttributes(ctx, tok, ku.ID)
			if err != nil {
				return nil, err
			}
			for k, v := range rolePerms {
				if v {
					attrs.Perms[k] = true
				}
			}
			if roleCRUD["canCreate"] {
				attrs.CanCreate = true
			}
			if roleCRUD["canRead"] {
				attrs.CanRead = true
			}
			if roleCRUD["canUpdate"] {
				attrs.CanUpdate = true
			}
			if roleCRUD["canDelete"] {
				attrs.CanDelete = true
			}
			attrs.Roles = roleNames

			out = append(out, &user.User{
				Session: "", Address: address, Attrs: attrs,
			})
			log.Default().Printf("[Keycloak] --> user %s roles: %v perms: %v", address, roleNames, attrs.Perms)
		}

		start += len(users)
		if len(users) < max {
			break
		}
	}
	log.Default().Printf("[Keycloak] --> fetched %d users", len(out))
	return out, nil
}

// ------------------ modelli minimal KC ------------------

type kcUser struct {
	ID         string              `json:"id"`
	Username   string              `json:"username"`
	Attributes map[string][]string `json:"attributes"`
}

// ------------------ chiamate KC ------------------

func (kc *KeycloakClient) ensureToken(ctx context.Context) (string, error) {
	kc.mu.Lock()
	defer kc.mu.Unlock()

	if kc.token != "" && time.Now().Add(30*time.Second).Before(kc.exp) {
		return kc.token, nil
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", kc.cfg.ClientID)
	form.Set("client_secret", kc.cfg.ClientSecret)

	endpoint := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := kc.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token http %d: %s", resp.StatusCode, string(b))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	kc.token = tok.AccessToken
	kc.exp = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return kc.token, nil
}

func (kc *KeycloakClient) listUsers(ctx context.Context, token string, first, max int) ([]kcUser, error) {
	endpoint := fmt.Sprintf("%s/admin/realms/%s/users?first=%d&max=%d",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm, first, max)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := kc.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("listUsers http %d: %s", resp.StatusCode, string(b))
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if !strings.Contains(ct, "application/json") {
		return nil, fmt.Errorf("content-type inatteso: %s", ct)
	}
	var users []kcUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}
	log.Default().Printf("[Keycloak] --> fetched %d users", len(users))
	return users, nil
}

func (kc *KeycloakClient) findUserByExactUsername(ctx context.Context, token, username string) (*kcUser, error) {
	// exact match: ?username=<user>&exact=true&briefRepresentation=true
	params := url.Values{
		"username":            {username},
		"exact":               {"true"},
		"briefRepresentation": {"true"},
		"max":                 {"2"},
	}
	endpoint := fmt.Sprintf("%s/admin/realms/%s/users?%s",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm, params.Encode())

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := kc.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("findUserByExactUsername http %d: %s", resp.StatusCode, string(b))
	}
	var users []kcUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}
	for _, u := range users {
		log.Default().Printf(" - user: %s attrs=%v", u.Username, u.Attributes)
	}
	if len(users) == 1 {
		return &users[0], nil
	}
	log.Default().Printf("[Keycloak] --> found %d users for username %s", len(users), username)
	return nil, nil
}

// Ricerca per attributo (Keycloak moderne supportano q=attributes.<name>:<value>).
// Se la tua versione non lo supporta, fai prima una listUsers con filtro di search
// e poi filtra client-side sugli Attributes.
func (kc *KeycloakClient) findUserByAttribute(ctx context.Context, token, attr, value string) (*kcUser, error) {
	q := url.Values{
		"q":                   {fmt.Sprintf("attributes.%s:%s", attr, value)},
		"briefRepresentation": {"true"},
		"max":                 {"5"},
	}
	endpoint := fmt.Sprintf("%s/admin/realms/%s/users?%s",
		strings.TrimRight(kc.cfg.BaseURL, "/"), kc.cfg.Realm, q.Encode())

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := kc.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("findUserByAttribute http %d: %s", resp.StatusCode, string(b))
	}
	var users []kcUser
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}
	for _, u := range users {
		if strings.EqualFold(first(u.Attributes[attr]), value) {
			return &u, nil
		}
	}
	log.Default().Printf("[Keycloak] --> found %d users for attribute %s=%s", len(users), attr, value)
	return nil, nil
}

// ------------------ mapping attributi KC ⇒ tuoi Attributes ------------------

func mapUserAttrsToCRUD(kcAttrs map[string][]string) *user.Attributes {
	// puoi salvare in Keycloak questi attributi a livello utente:
	// canCreate=true, canRead=true, canUpdate=false, canDelete=false
	asBool := func(key string) bool {
		vals := kcAttrs[key]
		if len(vals) == 0 {
			return false
		}
		v := strings.ToLower(strings.TrimSpace(vals[0]))
		return v == "true" || v == "1" || v == "yes"
	}
	log.Default().Printf("[Keycloak] --> mapping user attributes: %v", kcAttrs)
	return &user.Attributes{
		CanCreate: asBool("canCreate"),
		CanRead:   asBool("canRead"),
		CanUpdate: asBool("canUpdate"),
		CanDelete: asBool("canDelete"),
	}
}

// ------------------ helpers ------------------

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}
