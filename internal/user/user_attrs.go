package user

type Attributes struct {
	CanCreate bool
	CanRead   bool
	CanUpdate bool
	CanDelete bool

	Perms map[string]bool `json:"perms,omitempty"`
	Roles []string        `json:"roles,omitempty"`
}

type User struct {
	Session string
	Address string
	Attrs   *Attributes
}

func NewUser(session, address string) *User {
	return &User{
		Session: session,
		Address: address,
	}
}

func (u *User) Can(op string) bool {
	if u == nil || u.Attrs == nil || u.Attrs.Perms == nil {
		return false
	}
	return u.Attrs.Perms[op]
}
