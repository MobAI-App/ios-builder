package asc

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// RoleCustomerSupport is the least privileged team role; enough to be an
// internal TestFlight tester of the apps made visible to the user.
const RoleCustomerSupport = "CUSTOMER_SUPPORT"

// User is a member of the App Store Connect team.
type User struct {
	ID             string
	Email          string // ASC calls it "username"
	FirstName      string
	LastName       string
	Roles          []string
	AllAppsVisible bool
}

type userAttributes struct {
	Username       string   `json:"username,omitempty"`
	FirstName      string   `json:"firstName,omitempty"`
	LastName       string   `json:"lastName,omitempty"`
	Roles          []string `json:"roles,omitempty"`
	AllAppsVisible *bool    `json:"allAppsVisible,omitempty"`
}

func toUser(r Resource[userAttributes]) User {
	a := r.Attributes
	u := User{ID: r.ID, Email: a.Username, FirstName: a.FirstName, LastName: a.LastName, Roles: a.Roles}
	if a.AllAppsVisible != nil {
		u.AllAppsVisible = *a.AllAppsVisible
	}
	return u
}

// ListUsers lists the team's members by email.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	rs, err := getAll[userAttributes](ctx, c, "/v1/users", url.Values{"sort": {"username"}})
	if err != nil {
		return nil, err
	}
	users := make([]User, 0, len(rs))
	for _, r := range rs {
		users = append(users, toUser(r))
	}
	return users, nil
}

// FindUser returns the team member with that email, or nil.
func (c *Client) FindUser(ctx context.Context, email string) (*User, error) {
	rs, err := getAll[userAttributes](ctx, c, "/v1/users", url.Values{"filter[username]": {email}})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if strings.EqualFold(r.Attributes.Username, email) {
			u := toUser(r)
			return &u, nil
		}
	}
	return nil, nil
}

// UserInvitation is a pending invitation to join the team.
type UserInvitation struct {
	ID             string
	Email          string
	FirstName      string
	LastName       string
	Roles          []string
	ExpirationDate time.Time
}

type userInvitationAttributes struct {
	Email          string     `json:"email,omitempty"`
	FirstName      string     `json:"firstName,omitempty"`
	LastName       string     `json:"lastName,omitempty"`
	Roles          []string   `json:"roles,omitempty"`
	AllAppsVisible *bool      `json:"allAppsVisible,omitempty"`
	ExpirationDate *time.Time `json:"expirationDate,omitempty"`
}

func toUserInvitation(r Resource[userInvitationAttributes]) UserInvitation {
	a := r.Attributes
	inv := UserInvitation{ID: r.ID, Email: a.Email, FirstName: a.FirstName, LastName: a.LastName, Roles: a.Roles}
	if a.ExpirationDate != nil {
		inv.ExpirationDate = *a.ExpirationDate
	}
	return inv
}

// FindUserInvitation returns the pending team invitation for that email, or nil.
func (c *Client) FindUserInvitation(ctx context.Context, email string) (*UserInvitation, error) {
	rs, err := getAll[userInvitationAttributes](ctx, c, "/v1/userInvitations", url.Values{"filter[email]": {email}})
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		if strings.EqualFold(r.Attributes.Email, email) {
			inv := toUserInvitation(r)
			return &inv, nil
		}
	}
	return nil, nil
}

// UserInvitationSpec describes a person to invite to the team.
type UserInvitationSpec struct {
	Email     string
	FirstName string
	LastName  string
	Roles     []string
	// AllAppsVisible grants every app; otherwise only VisibleAppIDs.
	AllAppsVisible bool
	VisibleAppIDs  []string
}

// InviteUser sends a team invitation; App Store Connect emails the person.
func (c *Client) InviteUser(ctx context.Context, spec *UserInvitationSpec) (*UserInvitation, error) {
	req := Resource[userInvitationAttributes]{
		Type:       "userInvitations",
		Attributes: userInvitationAttributes{Email: spec.Email, FirstName: spec.FirstName, LastName: spec.LastName, Roles: spec.Roles, AllAppsVisible: &spec.AllAppsVisible},
	}
	if !spec.AllAppsVisible {
		req.Relationships = Relationships{"visibleApps": ToMany("apps", spec.VisibleAppIDs)}
	}
	r, err := post[userInvitationAttributes, userInvitationAttributes](ctx, c, "/v1/userInvitations", req)
	if err != nil {
		return nil, err
	}
	inv := toUserInvitation(*r)
	return &inv, nil
}
