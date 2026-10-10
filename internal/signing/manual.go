package signing

// The manual half of `signing setup`: a certificate and profiles the user
// already has, taken as they are. The CLI reads the files and prompts; a
// program embedding Builder hands the bytes over. Both come through here for
// the type check, the extension matching, the upload and builder.json.

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/MobAI-App/ios-builder/internal/config"
)

// ManualOptions drives SetupManual.
type ManualOptions struct {
	// Type is the distribution, as ManualType read it from the profile.
	Type Type
	// ProfileName is the builder.json profile written with the distribution;
	// empty means the distribution's own name.
	ProfileName string
	// P12 is the certificate with its private key; Password opens it.
	P12      []byte
	Password string
	// Profile is the app's .mobileprovision; ExtensionProfiles are the
	// extensions' by bundle ID, as MatchExtensionProfiles pairs them.
	Profile           []byte
	ExtensionProfiles map[string][]byte
	// Log receives progress lines; nil discards them.
	Log io.Writer
	// SaveConfig persists cfg; nil writes builder.json in the working directory.
	SaveConfig func(*config.Config) error
}

// ManualResult is what SetupManual reports. As with Setup, a failed upload is
// not an error: it is recorded here for the caller to report, and the
// profile is then left out of builder.json.
type ManualResult struct {
	// SigningSet is the suffix of the secrets written (DEVELOPMENT, AD_HOC,
	// STORE, ENTERPRISE).
	SigningSet      string `json:"signing_set"`
	SecretsUploaded bool   `json:"secrets_uploaded"`
	// GitHubUpload is "ok" or why the upload failed.
	GitHubUpload string `json:"github_upload"`
	// BuildProfile is the builder.json profile that builds with the set.
	BuildProfile string `json:"build_profile"`
	// ProfileWritten says BuildProfile was written to builder.json, which
	// happens only when the secrets were uploaded.
	ProfileWritten bool `json:"profile_written"`
	// ReplacedDistribution is the distribution the profile had before, when
	// it was a different one.
	ReplacedDistribution string `json:"replaced_distribution,omitempty"`
	// UploadError is the failed upload, nil when the secrets reached GitHub.
	UploadError error `json:"-"`
}

// SetupManual uploads a signing set the user supplied and records the
// profile in builder.json: `builder signing setup --certificate --profile`
// after its prompts. storeErr is a secret store that could not be built (no
// GitHub login), reported like a failed upload.
func SetupManual(ctx context.Context, store SecretStore, storeErr error, cfg *config.Config, opts *ManualOptions) (*ManualResult, error) {
	set, err := config.SigningSet(string(opts.Type))
	if err != nil {
		return nil, err
	}
	profileName := opts.ProfileName
	if profileName == "" {
		profileName = string(opts.Type)
	}
	res := &ManualResult{SigningSet: set, BuildProfile: profileName}
	res.UploadError = UploadSet(ctx, store, storeErr, cfg, opts.Log, set, opts.P12, opts.Password, opts.Profile, opts.ExtensionProfiles)
	res.SecretsUploaded = res.UploadError == nil
	res.GitHubUpload = "ok"
	if res.UploadError != nil {
		res.GitHubUpload = res.UploadError.Error()
	}

	// The profile is written only when its secrets reached the repository:
	// builder.json must not claim a signing set the repository does not have.
	if res.SecretsUploaded {
		res.ReplacedDistribution = WriteProfile(cfg, profileName, opts.Type)
		res.ProfileWritten = true
	}
	if err := saveConfig(opts.SaveConfig, cfg); err != nil {
		return res, err
	}
	return res, nil
}

// ManualType is what the .mobileprovision says it is. A distribution that
// disagrees is an error, since the runner refuses such a pair; empty takes
// the profile's word.
func ManualType(profileData []byte, distribution string) (Type, error) {
	typ, err := ProfileType(profileData)
	if err != nil {
		return "", err
	}
	if distribution == "" {
		return typ, nil
	}
	want, err := ParseType(distribution)
	if err != nil {
		return "", err
	}
	if want != typ {
		return "", fmt.Errorf("the profile is a %s profile, but --distribution %s was given; builds with distribution %s would refuse it", typ, want, want)
	}
	return typ, nil
}

// MatchExtensionProfiles pairs every extension in extensions with the key
// of the profile (name → contents) whose app id covers it. Each must be of
// the app profile's type; an extension without a profile, or a profile for
// no listed extension, is an error naming it.
func MatchExtensionProfiles(extensions []string, files map[string][]byte, typ Type) (map[string]string, error) {
	appIDs := make(map[string]string, len(files))
	for _, path := range slices.Sorted(maps.Keys(files)) {
		fileType, err := ProfileType(files[path])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if fileType != typ {
			return nil, fmt.Errorf("%s is a %s profile, but the app profile is %s; every extension profile must be of the same type", path, fileType, typ)
		}
		if appIDs[path], err = ProfileBundleID(files[path]); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// The longest app id is the most specific, so an exact profile wins over
	// a wildcard one covering the same extension.
	paths := slices.SortedFunc(maps.Keys(appIDs), func(a, b string) int {
		return len(appIDs[b]) - len(appIDs[a])
	})
	profiles := make(map[string]string, len(extensions))
	var problems []string
	for _, path := range paths {
		covered := false
		for _, id := range extensions {
			if Covers(appIDs[path], id) {
				covered = true
				if _, ok := profiles[id]; !ok {
					profiles[id] = path
				}
			}
		}
		if !covered {
			problems = append(problems, fmt.Sprintf("%s covers %s, which is not in ios.extensions", path, appIDs[path]))
		}
	}
	for _, id := range extensions {
		if _, ok := profiles[id]; !ok {
			problems = append(problems, fmt.Sprintf("extension %s has no profile; pass --extension-profile <mobileprovision> for it", id))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("extension profiles do not match ios.extensions in builder.json:\n  %s", strings.Join(problems, "\n  "))
	}
	return profiles, nil
}
