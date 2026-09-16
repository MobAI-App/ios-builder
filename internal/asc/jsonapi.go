package asc

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// Document is a JSON:API top-level document. T is a Resource for single
// resources and a []Resource for collections.
type Document[T any] struct {
	Data  T     `json:"data"`
	Links Links `json:"links,omitzero"`
	Meta  *Meta `json:"meta,omitempty"`
}

// Links carries pagination links.
type Links struct {
	Self string `json:"self,omitempty"`
	Next string `json:"next,omitempty"`
}

// Meta carries paging information on collections.
type Meta struct {
	Paging struct {
		Total int `json:"total"`
		Limit int `json:"limit"`
	} `json:"paging"`
}

// Resource is a JSON:API resource object with typed attributes.
type Resource[A any] struct {
	Type          string        `json:"type"`
	ID            string        `json:"id,omitempty"`
	Attributes    A             `json:"attributes,omitzero"`
	Relationships Relationships `json:"relationships,omitempty"`
}

// Relationships maps relationship names to their linkage.
type Relationships map[string]Relationship

// Relationship holds a to-one (object) or to-many (array) linkage.
type Relationship struct {
	Data json.RawMessage `json:"data,omitempty"`
}

// Linkage identifies a related resource.
type Linkage struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// ToOne builds a to-one relationship.
func ToOne(resourceType, id string) Relationship {
	data, _ := json.Marshal(Linkage{Type: resourceType, ID: id})
	return Relationship{Data: data}
}

// ToMany builds a to-many relationship.
func ToMany(resourceType string, ids []string) Relationship {
	linkages := make([]Linkage, 0, len(ids))
	for _, id := range ids {
		linkages = append(linkages, Linkage{Type: resourceType, ID: id})
	}
	data, _ := json.Marshal(linkages)
	return Relationship{Data: data}
}

// One decodes a to-one linkage; ok is false when the relationship is null or absent.
func (r Relationship) One() (linkage Linkage, ok bool) {
	if len(r.Data) == 0 || json.Unmarshal(r.Data, &linkage) != nil || linkage.ID == "" {
		return Linkage{}, false
	}
	return linkage, true
}

// One returns the named to-one linkage.
func (r Relationships) One(name string) (Linkage, bool) {
	rel, ok := r[name]
	if !ok {
		return Linkage{}, false
	}
	return rel.One()
}

// pageLimit is the largest page App Store Connect serves.
const pageLimit = 200

// getOne fetches a single resource.
func getOne[A any](ctx context.Context, c *Client, path string, query url.Values) (*Resource[A], error) {
	var doc Document[Resource[A]]
	if err := c.Get(ctx, path, query, &doc); err != nil {
		return nil, err
	}
	return &doc.Data, nil
}

// getAll fetches a collection, following links.next until exhausted.
func getAll[A any](ctx context.Context, c *Client, path string, query url.Values) ([]Resource[A], error) {
	if query == nil {
		query = url.Values{}
	}
	if query.Get("limit") == "" {
		query.Set("limit", strconv.Itoa(pageLimit))
	}
	var all []Resource[A]
	next := path
	for {
		var doc Document[[]Resource[A]]
		if err := c.Get(ctx, next, query, &doc); err != nil {
			return nil, err
		}
		all = append(all, doc.Data...)
		if doc.Links.Next == "" {
			return all, nil
		}
		// The next link already carries the filters and cursor.
		next, query = doc.Links.Next, nil
	}
}

// getPage fetches one page of a collection without following links.
func getPage[A any](ctx context.Context, c *Client, path string, query url.Values) ([]Resource[A], error) {
	var doc Document[[]Resource[A]]
	if err := c.Get(ctx, path, query, &doc); err != nil {
		return nil, err
	}
	return doc.Data, nil
}

// post creates a resource and decodes the created one.
func post[Req, Resp any](ctx context.Context, c *Client, path string, req Resource[Req]) (*Resource[Resp], error) {
	var doc Document[Resource[Resp]]
	if err := c.Post(ctx, path, Document[Resource[Req]]{Data: req}, &doc); err != nil {
		return nil, err
	}
	return &doc.Data, nil
}

// patch updates a resource and decodes the updated one.
func patch[Req, Resp any](ctx context.Context, c *Client, path string, req Resource[Req]) (*Resource[Resp], error) {
	var doc Document[Resource[Resp]]
	if err := c.Patch(ctx, path, Document[Resource[Req]]{Data: req}, &doc); err != nil {
		return nil, err
	}
	return &doc.Data, nil
}
