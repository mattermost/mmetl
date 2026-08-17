package slack_grid

import (
	"archive/zip"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/mattermost/mmetl/services/slack"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type teamArchiveFiles struct {
	users *zip.File
	posts []*zip.File
}

// DiscoverTeamMap infers Slack workspace ID -> teams/<name>/ folder mappings
// from the Grid export itself. Each teams/<name>/ folder is a nested workspace
// export; native members' team_id (or a post's team field) is the ID that
// shared-channel posts use in their "team" attribute.
func (t *GridTransformer) DiscoverTeamMap(zipReader *zip.Reader) (map[string]string, error) {
	folders := teamFolderNames(zipReader)
	if len(folders) == 0 {
		return nil, errors.New("no teams/ folders found in the export; is this an Enterprise Grid archive?")
	}

	indexed := indexTeamArchiveFiles(zipReader)
	result := make(map[string]string, len(folders))

	names := make([]string, 0, len(folders))
	for name := range folders {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, teamName := range names {
		if strings.Contains(teamName, "..") || strings.ContainsAny(teamName, "/\\") {
			t.Logger.WithField("team_name", teamName).Warn("skipping team folder with unsafe name")
			continue
		}

		files := indexed[teamName]
		teamID, source, err := t.discoverTeamID(files)
		if err != nil {
			return nil, err
		}
		if teamID == "" {
			t.Logger.WithField("team_name", teamName).Warn("could not infer Slack team ID for workspace folder")
			continue
		}

		if existing, ok := result[teamID]; ok && existing != teamName {
			return nil, errors.Errorf("Slack team ID %s maps to both %q and %q", teamID, existing, teamName)
		}

		t.Logger.WithFields(log.Fields{
			"team_id":   teamID,
			"team_name": teamName,
			"source":    source,
		}).Info("inferred team mapping")
		result[teamID] = teamName
	}

	if len(result) == 0 {
		return nil, errors.New("could not infer team mapping from the export; provide --team-map-path")
	}

	return result, nil
}

func (t *GridTransformer) discoverTeamID(files *teamArchiveFiles) (string, string, error) {
	if files == nil {
		return "", "", nil
	}

	var guestFallback string
	if files.users != nil {
		users, err := t.usersFromFile(files.users)
		if err != nil {
			t.Logger.WithError(err).WithField("file", files.users.Name).Warn("error reading users.json for team ID")
		} else {
			if teamID := majorityNativeTeamID(users); teamID != "" {
				return teamID, "users.json", nil
			}
			guestFallback = majorityAnyTeamID(users)
		}
	}

	for _, postFile := range files.posts {
		teamID, err := t.teamIDFromPostFile(postFile)
		if err != nil {
			t.Logger.WithError(err).WithField("file", postFile.Name).Warn("error reading posts for team ID")
			continue
		}
		if teamID != "" {
			return teamID, "posts", nil
		}
	}

	if guestFallback != "" {
		return guestFallback, "users.json", nil
	}

	return "", "", nil
}

func (t *GridTransformer) usersFromFile(file *zip.File) ([]slack.SlackUser, error) {
	rc, err := file.Open()
	if err != nil {
		return nil, errors.Wrap(err, "error opening users.json")
	}
	defer rc.Close()

	users, err := t.SlackParseUsers(rc)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// maxPostFileSize caps how large a single post file we'll decompress while
// inferring team IDs. Files used for this are small in practice; this just
// guards against a corrupt or maliciously crafted (e.g. zip-bomb) entry
// exhausting memory before discovery can fail gracefully.
const maxPostFileSize = 50 * 1024 * 1024 // 50MB

func (t *GridTransformer) teamIDFromPostFile(file *zip.File) (string, error) {
	if file.UncompressedSize64 > maxPostFileSize {
		return "", errors.Errorf("post file %s is %d bytes, exceeding the %d byte limit for team ID inference", file.Name, file.UncompressedSize64, maxPostFileSize)
	}

	rc, err := file.Open()
	if err != nil {
		return "", errors.Wrap(err, "error opening post file")
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		return "", errors.Wrap(err, "error reading post file")
	}

	return t.findTeamIDFromPostArray(content)
}

func indexTeamArchiveFiles(zipReader *zip.Reader) map[string]*teamArchiveFiles {
	indexed := make(map[string]*teamArchiveFiles)

	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() {
			continue
		}

		rest, ok := strings.CutPrefix(file.Name, "teams/")
		if !ok {
			continue
		}

		slash := strings.Index(rest, "/")
		if slash < 0 {
			continue
		}

		teamName := rest[:slash]
		remainder := rest[slash+1:]
		entry := indexed[teamName]
		if entry == nil {
			entry = &teamArchiveFiles{}
			indexed[teamName] = entry
		}

		if remainder == "users.json" {
			entry.users = file
			continue
		}

		// Channel posts live in teams/<name>/<channel>/YYYY-MM-DD.json.
		if strings.Contains(remainder, "/") && strings.HasSuffix(remainder, ".json") {
			entry.posts = append(entry.posts, file)
		}
	}

	return indexed
}

func majorityNativeTeamID(users []slack.SlackUser) string {
	return majorityTeamIDMatching(users, func(u slack.SlackUser) bool {
		return u.TeamID != "" && !u.Deleted && !u.IsBot && !u.IsGuest()
	})
}

func majorityAnyTeamID(users []slack.SlackUser) string {
	return majorityTeamIDMatching(users, func(u slack.SlackUser) bool {
		return u.TeamID != ""
	})
}

func majorityTeamIDMatching(users []slack.SlackUser, include func(slack.SlackUser) bool) string {
	counts := make(map[string]int)
	for _, u := range users {
		if include(u) {
			counts[u.TeamID]++
		}
	}

	best, bestN := "", 0
	tied := false
	for id, n := range counts {
		switch {
		case n > bestN:
			best, bestN, tied = id, n, false
		case n == bestN:
			tied = true
		}
	}
	if tied || bestN == 0 {
		return ""
	}
	return best
}

// FormatTeamMap returns a stable printable representation of a workspace ID to
// folder-name mapping.
func FormatTeamMap(teams map[string]string) string {
	ids := make([]string, 0, len(teams))
	for id := range teams {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "  %s -> %s\n", id, teams[id])
	}
	return b.String()
}
