package slack_grid

import (
	"archive/zip"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
)

func (t *GridTransformer) GridPreCheck(zipReader *zip.Reader) bool {
	requiredFiles := []string{
		"channels.json",
		"dms.json",
		"groups.json",
		"mpims.json",
	}

	valid := true

	for _, fileName := range requiredFiles {
		fileExists := t.CheckForRequiredFile(zipReader, fileName)
		valid = valid && fileExists
	}

	if len(t.Teams) == 0 {
		t.Logger.Error("no teams found in teams.json")
		valid = false
	}

	if !t.checkForDuplicateTeamNames() {
		valid = false
	}

	if !t.checkTeamFoldersExist(zipReader) {
		valid = false
	}

	return valid
}

// checkForDuplicateTeamNames ensures every team ID in teams.json maps to a
// distinct name. Without this, channels for teams sharing the same name
// would be silently merged into the same "teams/<name>/" output directory
// and zip file.
func (t *GridTransformer) checkForDuplicateTeamNames() bool {
	teamIDsByName := make(map[string][]string)
	for teamID, teamName := range t.Teams {
		teamIDsByName[teamName] = append(teamIDsByName[teamName], teamID)
	}

	valid := true
	for teamName, teamIDs := range teamIDsByName {
		if len(teamIDs) > 1 {
			sort.Strings(teamIDs)
			t.Logger.WithFields(log.Fields{
				"team_name": teamName,
				"team_ids":  teamIDs,
			}).Error("team name in teams.json is used for multiple team IDs")
			valid = false
		}
	}

	return valid
}

// checkTeamFoldersExist ensures every team name in teams.json has a
// corresponding "teams/<name>/" folder in the export archive. teams.json
// names must match the folders Slack already put in the export, not
// arbitrary display names.
func (t *GridTransformer) checkTeamFoldersExist(zipReader *zip.Reader) bool {
	valid := true
	for teamID, teamName := range t.Teams {
		prefix := "teams/" + teamName + "/"
		found := false
		for _, file := range zipReader.File {
			if strings.HasPrefix(file.Name, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Logger.WithFields(log.Fields{
				"team_name": teamName,
				"team_id":   teamID,
				"path":      prefix,
			}).Error("folder not found for team in the export archive")
			valid = false
		}
	}

	return valid
}
