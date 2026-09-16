package signing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/MobAI-App/ios-builder/internal/asc"
)

// Type is what the signing material is for: which certificate is issued and
// which profile type wraps it.
type Type string

// Signing types, as accepted by --type.
const (
	TypeDevelopment Type = "development"
	TypeAdHoc       Type = "ad-hoc"
	TypeAppStore    Type = "app-store"
)

// ParseType validates a --type value.
func ParseType(s string) (Type, error) {
	switch t := Type(strings.ToLower(strings.TrimSpace(s))); t {
	case TypeDevelopment, TypeAdHoc, TypeAppStore:
		return t, nil
	case "adhoc":
		return TypeAdHoc, nil
	case "appstore":
		return TypeAppStore, nil
	default:
		return "", fmt.Errorf("--type must be development, ad-hoc or app-store, got %q", s)
	}
}

// NeedsDevices reports whether profiles of this type list the devices the
// app may run on; App Store profiles do not.
func (t Type) NeedsDevices() bool { return t != TypeAppStore }

func (t Type) certificateType() string {
	if t == TypeDevelopment {
		return asc.CertificateTypeDevelopment
	}
	return asc.CertificateTypeDistribution
}

func (t Type) profileType() string {
	switch t {
	case TypeAdHoc:
		return asc.ProfileTypeIOSAppAdHoc
	case TypeAppStore:
		return asc.ProfileTypeIOSAppStore
	default:
		return asc.ProfileTypeIOSAppDevelopment
	}
}

// Device is a device to register, by UDID.
type Device struct {
	Name string `json:"name"`
	UDID string `json:"udid"`
}

// File names written to the output directory.
const (
	KeyFileName = "ios-signing.key"
	P12FileName = "ios-signing.p12"
)

// AutoOptions configures Auto.
type AutoOptions struct {
	BundleID string
	Type     Type
	// Devices are registered when missing; development and ad-hoc profiles
	// then cover every enabled iOS device on the account.
	Devices []Device
	// KeyPEM is an existing private key. When nil a key is generated and
	// written to OutDir/ios-signing.key.
	KeyPEM []byte
	// CommonName goes into the CSR subject of a new certificate.
	CommonName string
	// Password protects the .p12.
	Password string
	// Force issues a new certificate and profile even when valid ones exist.
	Force bool
	// OutDir receives the key, .p12 and .mobileprovision (default ".").
	OutDir string
	// Log receives progress; nil is silent.
	Log io.Writer
	// now is replaced by tests.
	now func() time.Time
}

// BundleIDResult reports the App ID used.
type BundleIDResult struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Created    bool   `json:"created"`
}

// CertificateResult reports the certificate the .p12 holds.
type CertificateResult struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	SerialNumber   string    `json:"serial_number"`
	Type           string    `json:"type"`
	ExpirationDate time.Time `json:"expiration_date"`
	Created        bool      `json:"created"`
	// ValidOnAccount counts unexpired certificates of this type before the
	// run, so a user hitting Apple's limit can see why.
	ValidOnAccount int `json:"valid_on_account"`
}

// DevicesResult reports device registration; empty for App Store.
type DevicesResult struct {
	Registered []Device `json:"registered"`
	// InProfile counts the enabled devices the profile covers.
	InProfile int `json:"in_profile"`
}

// ProfileResult reports the profile written.
type ProfileResult struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	UUID           string    `json:"uuid"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	ExpirationDate time.Time `json:"expiration_date"`
	Created        bool      `json:"created"`
	// Reason says why a profile was created ("missing", "invalid", "expired",
	// "certificate changed", "devices changed", "forced"); empty when reused.
	Reason string `json:"reason,omitempty"`
}

// Files lists what Auto wrote.
type Files struct {
	// Key is set only when a key was generated.
	Key     string `json:"key,omitempty"`
	P12     string `json:"p12"`
	Profile string `json:"profile"`
}

// AutoResult is what Auto found, created and wrote.
type AutoResult struct {
	Type        Type              `json:"type"`
	BundleID    BundleIDResult    `json:"bundle_id"`
	Certificate CertificateResult `json:"certificate"`
	Devices     DevicesResult     `json:"devices"`
	Profile     ProfileResult     `json:"profile"`
	Files       Files             `json:"files"`
	// P12 and ProfileContent are the bytes written, for uploading.
	P12            []byte `json:"-"`
	ProfileContent []byte `json:"-"`
}

// Auto provisions everything an iOS build needs to sign through the App Store
// Connect API: the App ID, a certificate whose private key is on this
// machine, the devices, and a profile tying them together. It is idempotent:
// a second run reuses what is valid and recreates only what is missing,
// expired, invalid or no longer matches. Nothing is ever revoked.
func Auto(ctx context.Context, client *asc.Client, opts *AutoOptions) (*AutoResult, error) {
	if opts.BundleID == "" {
		return nil, errors.New("bundle ID is required")
	}
	if _, err := ParseType(string(opts.Type)); err != nil {
		return nil, err
	}
	if opts.Password == "" {
		return nil, errors.New("a .p12 password is required")
	}
	if opts.OutDir == "" {
		opts.OutDir = "."
	}
	now := opts.now
	if now == nil {
		now = time.Now
	}
	res := &AutoResult{Type: opts.Type}

	// 1. Bundle ID
	bundle, err := client.BundleIDByIdentifier(ctx, opts.BundleID)
	if err != nil {
		return res, err
	}
	if bundle == nil {
		logf(opts.Log, "Registering App ID %s...", opts.BundleID)
		if bundle, err = client.CreateBundleID(ctx, opts.BundleID, bundleIDName(opts.BundleID), asc.PlatformIOS); err != nil {
			return res, fmt.Errorf("register App ID %s: %w", opts.BundleID, err)
		}
		res.BundleID.Created = true
	} else {
		logf(opts.Log, "App ID %s is registered (%s)", bundle.Identifier, bundle.Name)
	}
	res.BundleID.ID, res.BundleID.Identifier = bundle.ID, bundle.Identifier

	// 2. Certificate
	keyPEM := opts.KeyPEM
	if keyPEM == nil {
		if keyPEM, err = generateKey(); err != nil {
			return res, err
		}
		res.Files.Key = filepath.Join(opts.OutDir, KeyFileName)
	}
	cert, err := ensureCertificate(ctx, client, opts, keyPEM, now(), &res.Certificate)
	if err != nil {
		return res, err
	}
	res.P12, err = BuildP12(keyPEM, cert.Content, opts.Password)
	if err != nil {
		return res, err
	}

	// 3. Devices
	var deviceIDs []string
	if opts.Type.NeedsDevices() {
		if deviceIDs, err = ensureDevices(ctx, client, opts, &res.Devices); err != nil {
			return res, err
		}
	}

	// 4. Profile
	profile, err := ensureProfile(ctx, client, opts, bundle.ID, cert.ID, deviceIDs, now(), &res.Profile)
	if err != nil {
		return res, err
	}
	res.ProfileContent = profile.Content

	// 5. Files
	if err := os.MkdirAll(opts.OutDir, 0755); err != nil {
		return res, fmt.Errorf("create %s: %w", opts.OutDir, err)
	}
	if res.Files.Key != "" {
		if err := os.WriteFile(res.Files.Key, keyPEM, 0600); err != nil {
			return res, fmt.Errorf("write private key: %w", err)
		}
	}
	res.Files.P12 = filepath.Join(opts.OutDir, P12FileName)
	if err := os.WriteFile(res.Files.P12, res.P12, 0600); err != nil {
		return res, fmt.Errorf("write .p12: %w", err)
	}
	res.Files.Profile = filepath.Join(opts.OutDir, ProfileFileName(profile.Name))
	if err := os.WriteFile(res.Files.Profile, profile.Content, 0600); err != nil {
		return res, fmt.Errorf("write provisioning profile: %w", err)
	}
	return res, nil
}

// ProfileName is the portal name of the profile Auto manages for a bundle ID.
func ProfileName(t Type, bundleID string) string {
	return fmt.Sprintf("Builder %s %s", t, bundleID)
}

// ProfileFileName is the .mobileprovision file name for a profile name.
func ProfileFileName(profileName string) string {
	return strings.ReplaceAll(profileName, " ", "-") + ".mobileprovision"
}

// bundleIDName derives the App ID's display name, which the portal restricts
// to letters, digits and spaces.
func bundleIDName(identifier string) string {
	mapped := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return ' '
	}, identifier)
	return strings.Join(strings.Fields(mapped), " ")
}

func generateKey() ([]byte, error) {
	keyPEM, _, err := GenerateKeyAndCSR("Builder", "")
	return keyPEM, err
}

// ensureCertificate reuses a valid certificate issued for the key, else has
// Apple issue one. Certificates without their key on this machine cannot go
// into a .p12, so they are ignored rather than revoked.
func ensureCertificate(ctx context.Context, client *asc.Client, opts *AutoOptions, keyPEM []byte, now time.Time, out *CertificateResult) (*asc.Certificate, error) {
	certType := opts.Type.certificateType()
	certs, err := client.ListCertificates(ctx, certType)
	if err != nil {
		return nil, err
	}
	var valid []asc.Certificate
	for i := range certs {
		if certs[i].ExpirationDate.After(now) {
			valid = append(valid, certs[i])
		}
	}
	out.ValidOnAccount = len(valid)
	if !opts.Force && opts.KeyPEM != nil {
		for i := range valid {
			if KeyMatchesCertificate(keyPEM, valid[i].Content) {
				logf(opts.Log, "Reusing %s certificate %s (expires %s)", certType, valid[i].Name, valid[i].ExpirationDate.Format("2006-01-02"))
				fillCertificate(out, &valid[i], false)
				return &valid[i], nil
			}
		}
		logf(opts.Log, "%d valid %s certificate(s) on the account, none issued for the private key", len(valid), certType)
	} else if len(valid) > 0 && !opts.Force {
		logf(opts.Log, "%d valid %s certificate(s) on the account, but their private keys are not on this machine", len(valid), certType)
	}

	commonName := opts.CommonName
	if commonName == "" {
		commonName = "Builder"
	}
	csr, err := CreateCSR(keyPEM, commonName, "")
	if err != nil {
		return nil, err
	}
	logf(opts.Log, "Requesting a new %s certificate...", certType)
	cert, err := client.CreateCertificate(ctx, certType, csr)
	if err != nil {
		return nil, withLimitHint(err, "Apple limits a team to 2 Apple Development and 3 Apple Distribution certificates. Revoke one you no longer use at https://developer.apple.com/account/resources/certificates/list (Builder never revokes anything), or pass --key with the private key of an existing certificate to reuse it.")
	}
	if !KeyMatchesCertificate(keyPEM, cert.Content) {
		return nil, fmt.Errorf("certificate %s from App Store Connect was not issued for the private key", cert.ID)
	}
	fillCertificate(out, cert, true)
	return cert, nil
}

func fillCertificate(out *CertificateResult, c *asc.Certificate, created bool) {
	out.ID, out.Name, out.SerialNumber, out.Type, out.ExpirationDate, out.Created = c.ID, c.Name, c.SerialNumber, c.Type, c.ExpirationDate, created
}

// ensureDevices registers the missing devices and returns the IDs of every
// enabled iOS device on the account, sorted, which is what the profile covers.
func ensureDevices(ctx context.Context, client *asc.Client, opts *AutoOptions, out *DevicesResult) ([]string, error) {
	devices, err := client.ListDevices(ctx, asc.PlatformIOS)
	if err != nil {
		return nil, err
	}
	byUDID := make(map[string]asc.Device, len(devices))
	for _, d := range devices {
		byUDID[strings.ToUpper(d.UDID)] = d
	}
	out.Registered = []Device{}
	for _, want := range opts.Devices {
		udid := strings.TrimSpace(want.UDID)
		if udid == "" {
			continue
		}
		if existing, ok := byUDID[strings.ToUpper(udid)]; ok {
			logf(opts.Log, "Device %s is registered as %q (%s)", udid, existing.Name, strings.ToLower(existing.Status))
			continue
		}
		name := strings.TrimSpace(want.Name)
		if name == "" {
			name = "iPhone " + udid[max(0, len(udid)-6):]
		}
		logf(opts.Log, "Registering device %q (%s)...", name, udid)
		d, err := client.RegisterDevice(ctx, name, udid, asc.PlatformIOS)
		if err != nil {
			return nil, withLimitHint(fmt.Errorf("register device %s: %w", udid, err), "Apple allows 100 iOS devices per membership year and frees no slot when one is removed; the count resets when the membership renews. Disable unused devices at https://developer.apple.com/account/resources/devices/list to keep them out of profiles.")
		}
		byUDID[strings.ToUpper(d.UDID)] = *d
		out.Registered = append(out.Registered, Device{Name: d.Name, UDID: d.UDID})
	}
	var ids []string
	for _, d := range byUDID {
		if d.Status == asc.DeviceStatusEnabled {
			ids = append(ids, d.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no iOS devices are registered on the account and a %s profile needs at least one: pass --device <udid> (repeatable) or --devices-from-mobai", opts.Type)
	}
	slices.Sort(ids)
	out.InProfile = len(ids)
	logf(opts.Log, "%d enabled device(s) will be in the profile", len(ids))
	return ids, nil
}

// ensureProfile reuses the Builder-managed profile when it is ACTIVE,
// unexpired and still lists exactly this certificate and these devices;
// otherwise it deletes and recreates it. Same-named duplicates go too.
func ensureProfile(ctx context.Context, client *asc.Client, opts *AutoOptions, bundleResourceID, certID string, deviceIDs []string, now time.Time, out *ProfileResult) (*asc.Profile, error) {
	name := ProfileName(opts.Type, opts.BundleID)
	profileType := opts.Type.profileType()
	existing, err := client.ListProfilesByName(ctx, name)
	if err != nil {
		return nil, err
	}
	reason := "missing"
	if len(existing) > 0 {
		p := &existing[0]
		if reason, err = recreateReason(ctx, client, opts, p, certID, deviceIDs, now); err != nil {
			return nil, err
		}
		if reason == "" {
			logf(opts.Log, "Reusing profile %q (%s, expires %s)", p.Name, strings.ToLower(p.State), p.ExpirationDate.Format("2006-01-02"))
			fillProfile(out, p, false, "")
			return p, nil
		}
		logf(opts.Log, "Recreating profile %q: %s", name, reason)
		for i := range existing {
			if err := client.DeleteProfile(ctx, existing[i].ID); err != nil {
				return nil, fmt.Errorf("delete profile %s: %w", existing[i].ID, err)
			}
		}
	} else {
		logf(opts.Log, "Creating profile %q...", name)
	}
	if !opts.Type.NeedsDevices() {
		deviceIDs = nil
	}
	p, err := client.CreateProfile(ctx, name, profileType, bundleResourceID, []string{certID}, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("create profile %q: %w", name, err)
	}
	if len(p.Content) == 0 {
		return nil, fmt.Errorf("profile %s from App Store Connect has no content", p.ID)
	}
	fillProfile(out, p, true, reason)
	return p, nil
}

// recreateReason says why the profile cannot be reused, or "" when it can.
func recreateReason(ctx context.Context, client *asc.Client, opts *AutoOptions, p *asc.Profile, certID string, deviceIDs []string, now time.Time) (string, error) {
	switch {
	case opts.Force:
		return "forced", nil
	case p.State != asc.ProfileStateActive:
		return strings.ToLower(p.State), nil
	case !p.ExpirationDate.IsZero() && !p.ExpirationDate.After(now):
		return "expired", nil
	case p.Type != opts.Type.profileType():
		return "type changed", nil
	case len(p.Content) == 0:
		return "no content", nil
	}
	certIDs, err := client.ProfileCertificateIDs(ctx, p.ID)
	if err != nil {
		return "", err
	}
	if len(certIDs) != 1 || certIDs[0] != certID {
		return "certificate changed", nil
	}
	if opts.Type.NeedsDevices() {
		have, err := client.ProfileDeviceIDs(ctx, p.ID)
		if err != nil {
			return "", err
		}
		slices.Sort(have)
		if !slices.Equal(have, deviceIDs) {
			return "devices changed", nil
		}
	}
	return "", nil
}

func fillProfile(out *ProfileResult, p *asc.Profile, created bool, reason string) {
	out.ID, out.Name, out.UUID, out.Type, out.State, out.ExpirationDate, out.Created, out.Reason = p.ID, p.Name, p.UUID, p.Type, p.State, p.ExpirationDate, created, reason
}

// withLimitHint appends hint when App Store Connect refused for a quota.
func withLimitHint(err error, hint string) error {
	var apiErr *asc.Error
	if !errors.As(err, &apiErr) {
		return err
	}
	for _, d := range apiErr.Errors {
		text := strings.ToLower(d.Title + " " + d.Detail)
		if strings.Contains(text, "maximum") || strings.Contains(text, "limit") || strings.Contains(text, "already have") {
			return fmt.Errorf("%w\n%s", err, hint)
		}
	}
	return err
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}
