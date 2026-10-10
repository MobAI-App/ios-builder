// Package github exposes the GitHub REST client to code outside this module.
package github

import "github.com/MobAI-App/ios-builder/internal/github"

const (
	// BaseURL is the GitHub API base URL.
	BaseURL = github.BaseURL

	// APIVersion is the GitHub API version header value.
	APIVersion = github.APIVersion
)

type (
	Client         = github.Client
	Repository     = github.Repository
	WorkflowRun    = github.WorkflowRun
	Job            = github.Job
	JobStep        = github.JobStep
	Artifact       = github.Artifact
	PublicKey      = github.PublicKey
	APIError       = github.APIError
	ProgressFunc   = github.ProgressFunc
	FileContent    = github.FileContent
	PutFileRequest = github.PutFileRequest
	FileCommit     = github.FileCommit
)

// ErrFileNotFound is Client.GetFile's answer for a path the branch does not have.
var ErrFileNotFound = github.ErrFileNotFound

// NewClient creates a GitHub API client authenticated with token.
func NewClient(token string) *Client {
	return github.NewClient(token)
}

// EncryptSecret encrypts a value using NaCl sealed box for GitHub secrets.
// publicKey should be base64-encoded, as returned by Client.GetPublicKey.
func EncryptSecret(publicKey, value string) (string, error) {
	return github.EncryptSecret(publicKey, value)
}
