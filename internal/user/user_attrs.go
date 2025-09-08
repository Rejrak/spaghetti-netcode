package user

type Attributes struct {
	CanCreate bool
	CanRead   bool
	CanUpdate bool
	CanDelete bool
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
		Attrs:   nil,
	}
}
