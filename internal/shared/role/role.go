package role

type Role string

const (
	Admin   Role = "admin"
	Manager Role = "manager"
)

func (r Role) String() string {
	return string(r)
}
