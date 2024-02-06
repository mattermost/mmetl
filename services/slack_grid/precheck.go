package slack_grid

import "archive/zip"

func (t *BulkTransformer) GridPreCheck(zipReader *zip.Reader) bool {
	requiredFiles := []string{
		// "org_users.json",
		"channels.json",
		"dms.json",
		"groups.json",
		"mpims.json",
	}

	valid := true

	for _, fileName := range requiredFiles {
		fileExists := t.Transformer.CheckForRequiredFile(zipReader, fileName)

		valid = valid && fileExists
	}

	return valid
}
