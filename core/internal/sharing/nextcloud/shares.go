package nextcloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Share types of the OCS Share API.
const (
	ShareTypeUser  = 0
	ShareTypeGroup = 1
	ShareTypeLink  = 3
	ShareTypeEmail = 4
)

// Permission bits.
const (
	PermRead   = 1
	PermUpdate = 2
	PermCreate = 4
	PermDelete = 8
	PermShare  = 16
)

// flexString decodes JSON strings, numbers and null into a string.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flexString(strings.Trim(string(b), `"`))
	return nil
}

// flexInt decodes JSON numbers and numeric strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

// flexBool decodes JSON booleans, 0/1 and "0"/"1".
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	switch strings.Trim(string(b), `"`) {
	case "true", "1":
		*f = true
	default:
		*f = false
	}
	return nil
}

// RawShare is one element of the OCS share list.
type RawShare struct {
	ID                   flexString `json:"id"`
	ShareType            flexInt    `json:"share_type"`
	Permissions          flexInt    `json:"permissions"`
	Path                 flexString `json:"path"`
	ItemType             flexString `json:"item_type"`
	Token                flexString `json:"token"`
	URL                  flexString `json:"url"`
	ShareWith            flexString `json:"share_with"`
	ShareWithDisplayname flexString `json:"share_with_displayname"`
	Expiration           flexString `json:"expiration"`
	Password             flexString `json:"password"`
	HideDownload         flexBool   `json:"hide_download"`
	Label                flexString `json:"label"`
	Note                 flexString `json:"note"`
	STime                flexInt    `json:"stime"`
	Attributes           flexString `json:"attributes"`
}

// Share is the normalised share (RPC contract "Share").
type Share struct {
	ID                   string  `json:"id"`
	Kind                 string  `json:"kind"`
	Path                 string  `json:"path"`
	ItemType             string  `json:"itemType"`
	URL                  string  `json:"url"`
	Token                string  `json:"token"`
	ShareWith            string  `json:"shareWith"`
	ShareWithDisplayName string  `json:"shareWithDisplayName"`
	Permissions          int     `json:"permissions"`
	ExpireDate           *string `json:"expireDate"`
	HasPassword          bool    `json:"hasPassword"`
	HideDownload         bool    `json:"hideDownload"`
	Label                string  `json:"label"`
	Note                 string  `json:"note"`
	CreatedAt            int64   `json:"createdAt"`
}

func kindOf(shareType int) string {
	switch shareType {
	case ShareTypeUser:
		return "user"
	case ShareTypeGroup:
		return "group"
	case ShareTypeEmail:
		return "email"
	case ShareTypeLink:
		return "publicLink"
	}
	return "other"
}

// Normalize converts a raw share. Paths are returned as given by the server.
func (r RawShare) Normalize() Share {
	s := Share{
		ID: string(r.ID), Kind: kindOf(int(r.ShareType)), Path: string(r.Path), ItemType: string(r.ItemType),
		URL: string(r.URL), Token: string(r.Token), ShareWith: string(r.ShareWith),
		ShareWithDisplayName: string(r.ShareWithDisplayname), Permissions: int(r.Permissions),
		HasPassword: r.Password != "", HideDownload: bool(r.HideDownload), Label: string(r.Label), Note: string(r.Note),
		CreatedAt: int64(r.STime) * 1000,
	}
	if s.Kind == "publicLink" && s.URL == "" && s.Token != "" {
		s.URL = "" // filled by the caller who knows the base URL
	}
	if e := string(r.Expiration); e != "" {
		d := e
		if len(d) >= 10 {
			d = d[:10]
		}
		s.ExpireDate = &d
	}
	if s.Kind == "publicLink" && strings.Contains(string(r.Attributes), `"download"`) && strings.Contains(string(r.Attributes), `"value":false`) {
		s.HideDownload = true
	}
	if s.ItemType == "" {
		s.ItemType = "file"
	}
	return s
}

const sharesPath = "/ocs/v2.php/apps/files_sharing/api/v1/shares"

// ListShares lists the user's shares, optionally for one OCS path (reshares included).
func (c *Client) ListShares(ctx context.Context, ocsPath string) ([]Share, error) {
	q := url.Values{}
	if ocsPath != "" {
		q.Set("path", ocsPath)
		q.Set("reshares", "true")
	}
	var raw []RawShare
	p := sharesPath
	if len(q) > 0 {
		p += "?" + q.Encode()
	}
	if err := c.ocs(ctx, http.MethodGet, p, nil, &raw); err != nil {
		// A path without shares answers 404 on some versions.
		var oe *OCSError
		if ocsPath != "" && asOCS(err, &oe) && (oe.StatusCode == 404 || oe.HTTPStatus == 404) {
			return []Share{}, nil
		}
		return nil, err
	}
	out := make([]Share, 0, len(raw))
	for _, r := range raw {
		out = append(out, c.fill(r.Normalize()))
	}
	return out, nil
}

func (c *Client) fill(s Share) Share {
	if s.Kind == "publicLink" && s.URL == "" && s.Token != "" {
		s.URL = c.Base + "/s/" + s.Token
	}
	return s
}

func asOCS(err error, target **OCSError) bool {
	oe, ok := err.(*OCSError)
	if ok {
		*target = oe
	}
	return ok
}

// CreateParams are the fields of a new share.
type CreateParams struct {
	Path         string
	ShareType    int
	ShareWith    string
	Permissions  int // 0 = server default
	Password     string
	ExpireDate   string // YYYY-MM-DD
	HideDownload bool
	Label        string
	Note         string
	SendMail     *bool
}

func hideDownloadAttr(hide bool) string {
	b, _ := json.Marshal([]map[string]any{{"scope": "permissions", "key": "download", "value": !hide}})
	return string(b)
}

// CreateShare creates a share.
func (c *Client) CreateShare(ctx context.Context, p CreateParams) (Share, error) {
	f := url.Values{}
	f.Set("path", p.Path)
	f.Set("shareType", strconv.Itoa(p.ShareType))
	if p.ShareWith != "" {
		f.Set("shareWith", p.ShareWith)
	}
	if p.Permissions > 0 {
		f.Set("permissions", strconv.Itoa(p.Permissions))
	}
	if p.Password != "" {
		f.Set("password", p.Password)
	}
	if p.ExpireDate != "" {
		f.Set("expireDate", p.ExpireDate)
	}
	if p.Label != "" {
		f.Set("label", p.Label)
	}
	if p.Note != "" {
		f.Set("note", p.Note)
	}
	if p.SendMail != nil {
		f.Set("sendMail", strconv.FormatBool(*p.SendMail))
	}
	if p.HideDownload {
		f.Set("attributes", hideDownloadAttr(true))
		f.Set("hideDownload", "true")
	}
	var raw RawShare
	if err := c.ocs(ctx, http.MethodPost, sharesPath, f, &raw); err != nil {
		return Share{}, err
	}
	return c.fill(raw.Normalize()), nil
}

// UpdateParams are optional share changes (nil = unchanged).
type UpdateParams struct {
	Password     *string
	ExpireDate   *string // "" clears
	Permissions  *int
	HideDownload *bool
	Label        *string
	Note         *string
}

// UpdateShare changes a share. The OCS API accepts one field per call on
// older servers, so each field is sent separately.
func (c *Client) UpdateShare(ctx context.Context, id string, p UpdateParams) (Share, error) {
	var fields []url.Values
	add := func(k, v string) { fields = append(fields, url.Values{k: {v}}) }
	if p.Password != nil {
		add("password", *p.Password)
	}
	if p.ExpireDate != nil {
		add("expireDate", *p.ExpireDate)
	}
	if p.Permissions != nil {
		add("permissions", strconv.Itoa(*p.Permissions))
	}
	if p.HideDownload != nil {
		fields = append(fields, url.Values{"attributes": {hideDownloadAttr(*p.HideDownload)}, "hideDownload": {strconv.FormatBool(*p.HideDownload)}})
	}
	if p.Label != nil {
		add("label", *p.Label)
	}
	if p.Note != nil {
		add("note", *p.Note)
	}
	var raw RawShare
	for _, f := range fields {
		if err := c.ocs(ctx, http.MethodPut, sharesPath+"/"+url.PathEscape(id), f, &raw); err != nil {
			return Share{}, err
		}
	}
	if len(fields) == 0 {
		if err := c.ocs(ctx, http.MethodGet, sharesPath+"/"+url.PathEscape(id), nil, &[]RawShare{}); err != nil {
			return Share{}, err
		}
	}
	return c.GetShare(ctx, id)
}

// GetShare fetches one share.
func (c *Client) GetShare(ctx context.Context, id string) (Share, error) {
	var raw []RawShare
	if err := c.ocs(ctx, http.MethodGet, sharesPath+"/"+url.PathEscape(id), nil, &raw); err != nil {
		return Share{}, err
	}
	if len(raw) == 0 {
		return Share{}, fmt.Errorf("share %s not found", id)
	}
	return c.fill(raw[0].Normalize()), nil
}

// DeleteShare removes a share.
func (c *Client) DeleteShare(ctx context.Context, id string) error {
	return c.ocs(ctx, http.MethodDelete, sharesPath+"/"+url.PathEscape(id), nil, nil)
}

// Sharee is a search hit for user/group/email shares.
type Sharee struct {
	Label     string `json:"label"`
	ShareType int    `json:"shareType"`
	ShareWith string `json:"shareWith"`
}

type rawSharee struct {
	Label string `json:"label"`
	Value struct {
		ShareType flexInt    `json:"shareType"`
		ShareWith flexString `json:"shareWith"`
	} `json:"value"`
}

// SearchSharees searches users, groups and emails.
func (c *Client) SearchSharees(ctx context.Context, search, itemType string) ([]Sharee, error) {
	q := url.Values{"search": {search}, "itemType": {itemType}, "perPage": {"20"}}
	var d struct {
		Exact struct {
			Users  []rawSharee `json:"users"`
			Groups []rawSharee `json:"groups"`
			Emails []rawSharee `json:"emails"`
		} `json:"exact"`
		Users  []rawSharee `json:"users"`
		Groups []rawSharee `json:"groups"`
		Emails []rawSharee `json:"emails"`
	}
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v1.php/apps/files_sharing/api/v1/sharees?"+q.Encode(), nil, &d); err != nil {
		return nil, err
	}
	out := []Sharee{}
	seen := map[string]bool{}
	for _, list := range [][]rawSharee{d.Exact.Users, d.Exact.Groups, d.Exact.Emails, d.Users, d.Groups, d.Emails} {
		for _, r := range list {
			key := strconv.Itoa(int(r.Value.ShareType)) + ":" + string(r.Value.ShareWith)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Sharee{Label: r.Label, ShareType: int(r.Value.ShareType), ShareWith: string(r.Value.ShareWith)})
		}
	}
	return out, nil
}

// Policy is the server's public-link policy.
type Policy struct {
	PasswordEnforced   bool `json:"passwordEnforced"`
	ExpireDateEnforced bool `json:"expireDateEnforced"`
	ExpireDateDays     int  `json:"expireDateDays"`
	DefaultExpireDate  bool `json:"defaultExpireDate"`
}

// Policy reads files_sharing.public from the capabilities.
func (c *Client) Policy(ctx context.Context) (Policy, error) {
	var d struct {
		Capabilities struct {
			FilesSharing struct {
				Public struct {
					Password struct {
						Enforced flexBool `json:"enforced"`
					} `json:"password"`
					ExpireDate struct {
						Enabled  flexBool `json:"enabled"`
						Enforced flexBool `json:"enforced"`
						Days     flexInt  `json:"days"`
					} `json:"expire_date"`
				} `json:"public"`
			} `json:"files_sharing"`
		} `json:"capabilities"`
	}
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v1.php/cloud/capabilities", nil, &d); err != nil {
		return Policy{}, err
	}
	pub := d.Capabilities.FilesSharing.Public
	return Policy{
		PasswordEnforced:   bool(pub.Password.Enforced),
		ExpireDateEnforced: bool(pub.ExpireDate.Enforced),
		ExpireDateDays:     int(pub.ExpireDate.Days),
		DefaultExpireDate:  bool(pub.ExpireDate.Enabled),
	}, nil
}

// IsPolicyError reports whether an OCS failure is caused by a server policy
// (enforced password or expiration).
func IsPolicyError(err error) bool {
	oe, ok := err.(*OCSError)
	if !ok {
		return false
	}
	m := strings.ToLower(oe.Message)
	return strings.Contains(m, "password") || strings.Contains(m, "expir")
}
