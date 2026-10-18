package auth

type UserAuthState struct {
	ID                int64
	Username          string
	RoleID            int
	Status            int
	PasswordChangedAt int64
}
