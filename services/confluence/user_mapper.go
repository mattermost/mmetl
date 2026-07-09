// Copyright (c) 2015-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package confluence

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var (
	ErrUserNotFound = errors.New("user not found in mapping")
	ErrInvalidCSV   = errors.New("invalid CSV format")
	ErrEmptyCSV     = errors.New("CSV contains no valid mappings")
)

// UserMapping represents a mapping from Confluence user to Mattermost user.
type UserMapping struct {
	ConfluenceAccountID string
	ConfluenceUsername  string
	ConfluenceEmail     string
	MattermostUsername  string
	MattermostUserID    string
}

// UserMapper handles mapping Confluence users to Mattermost users.
type UserMapper struct {
	// Mappings by various keys for flexible lookup
	byAccountID map[string]*UserMapping
	byUsername  map[string]*UserMapping
	byEmail     map[string]*UserMapping

	// Fallback username for unmapped users
	fallbackUsername string
}

// NewUserMapper creates a new user mapper from a CSV file.
// CSV format: confluence_account_id,confluence_username,confluence_email,mattermost_username,mattermost_user_id
// Header row is optional but recommended.
func NewUserMapper(csvPath string, fallbackUsername string) (*UserMapper, error) {
	um := &UserMapper{
		byAccountID:      make(map[string]*UserMapping),
		byUsername:       make(map[string]*UserMapping),
		byEmail:          make(map[string]*UserMapping),
		fallbackUsername: fallbackUsername,
	}

	if csvPath == "" {
		return um, nil
	}

	file, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open user mapping CSV: %w", err)
	}
	defer file.Close()

	if err := um.loadFromCSV(file); err != nil {
		return nil, err
	}

	return um, nil
}

// loadFromCSV loads mappings from a CSV reader.
func (um *UserMapper) loadFromCSV(r io.Reader) error {
	reader := csv.NewReader(r)
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1 // Allow variable fields

	lineNum := 0
	hasValidMappings := false

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: line %d: %v", ErrInvalidCSV, lineNum+1, err)
		}
		lineNum++

		// Skip header row
		if lineNum == 1 && isHeaderRow(record) {
			continue
		}

		// Parse record
		mapping, err := parseCSVRecord(record)
		if err != nil {
			// Log warning but continue
			continue
		}

		// Index by available keys
		if mapping.ConfluenceAccountID != "" {
			um.byAccountID[mapping.ConfluenceAccountID] = mapping
		}
		if mapping.ConfluenceUsername != "" {
			um.byUsername[strings.ToLower(mapping.ConfluenceUsername)] = mapping
		}
		if mapping.ConfluenceEmail != "" {
			um.byEmail[strings.ToLower(mapping.ConfluenceEmail)] = mapping
		}

		hasValidMappings = true
	}

	if !hasValidMappings {
		return ErrEmptyCSV
	}

	return nil
}

// isHeaderRow checks if a CSV row is a header.
func isHeaderRow(record []string) bool {
	if len(record) == 0 {
		return false
	}
	first := strings.ToLower(record[0])
	return strings.Contains(first, "account") ||
		strings.Contains(first, "confluence") ||
		strings.Contains(first, "user") ||
		strings.Contains(first, "id")
}

// parseCSVRecord parses a CSV record into a UserMapping.
func parseCSVRecord(record []string) (*UserMapping, error) {
	if len(record) < 4 {
		return nil, fmt.Errorf("record has too few fields: %d", len(record))
	}

	mapping := &UserMapping{
		ConfluenceAccountID: strings.TrimSpace(record[0]),
		ConfluenceUsername:  strings.TrimSpace(record[1]),
		ConfluenceEmail:     strings.TrimSpace(record[2]),
		MattermostUsername:  strings.TrimSpace(record[3]),
	}

	if len(record) > 4 {
		mapping.MattermostUserID = strings.TrimSpace(record[4])
	}

	// At minimum need a Mattermost username
	if mapping.MattermostUsername == "" {
		return nil, errors.New("missing mattermost username")
	}

	return mapping, nil
}

// GetUsername returns the Mattermost username for a Confluence account ID.
func (um *UserMapper) GetUsername(confluenceAccountID string) (string, error) {
	if mapping, ok := um.byAccountID[confluenceAccountID]; ok {
		return mapping.MattermostUsername, nil
	}
	return "", ErrUserNotFound
}

// GetUsernameByEmail returns the Mattermost username for a Confluence email.
func (um *UserMapper) GetUsernameByEmail(email string) (string, error) {
	if mapping, ok := um.byEmail[strings.ToLower(email)]; ok {
		return mapping.MattermostUsername, nil
	}
	return "", ErrUserNotFound
}

// GetUsernameByConfluenceUsername returns the Mattermost username for a Confluence username.
func (um *UserMapper) GetUsernameByConfluenceUsername(username string) (string, error) {
	if mapping, ok := um.byUsername[strings.ToLower(username)]; ok {
		return mapping.MattermostUsername, nil
	}
	return "", ErrUserNotFound
}

// ResolveUser attempts to resolve a Confluence user to a Mattermost username.
// It tries account ID, email, and username in order.
func (um *UserMapper) ResolveUser(accountID, email, username string) string {
	// Try account ID first
	if accountID != "" {
		if mmUser, err := um.GetUsername(accountID); err == nil {
			return mmUser
		}
	}

	// Try email
	if email != "" {
		if mmUser, err := um.GetUsernameByEmail(email); err == nil {
			return mmUser
		}
	}

	// Try username
	if username != "" {
		if mmUser, err := um.GetUsernameByConfluenceUsername(username); err == nil {
			return mmUser
		}
	}

	// Return fallback
	if um.fallbackUsername != "" {
		return um.fallbackUsername
	}

	return ""
}

// GetMappingCount returns the number of user mappings loaded.
func (um *UserMapper) GetMappingCount() int {
	return len(um.byAccountID)
}

// GetEmailMappingCount returns the number of email mappings loaded.
func (um *UserMapper) GetEmailMappingCount() int {
	return len(um.byEmail)
}

// GetFallbackUsername returns the fallback username.
func (um *UserMapper) GetFallbackUsername() string {
	return um.fallbackUsername
}

// ValidateMappings checks that all mappings have valid Mattermost usernames.
// Returns a list of warnings for any issues found.
func (um *UserMapper) ValidateMappings() []string {
	var warnings []string

	for accountID, mapping := range um.byAccountID {
		if mapping.MattermostUsername == "" {
			warnings = append(warnings, fmt.Sprintf("Account %s has no Mattermost username", accountID))
		}
	}

	return warnings
}
