package slack

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/text/unicode/norm"

	"github.com/mattermost/mmetl/services/intermediate"
)

const attachmentsInternal = "bulk-export-attachments"

// exitFunc is the function called for fatal errors. Overridable in tests.
var exitFunc func(code int) = os.Exit

// isValidChannelNameCharacters delegates to the shared package validator.
// Kept here for backward compatibility with tests in this package.
var isValidChannelNameCharacters = intermediate.IsValidChannelNameCharacters

// Type aliases for backward compatibility — all types are now defined in the
// shared intermediate package and re-exported here.
type IntermediateChannel = intermediate.IntermediateChannel
type IntermediateUser = intermediate.IntermediateUser
type IntermediateReaction = intermediate.IntermediateReaction
type IntermediatePost = intermediate.IntermediatePost
type Intermediate = intermediate.Intermediate

// syncExitFunc synchronises the slack-package exitFunc with the shared package's
// ExitFunc so that both packages use the same override in tests.
func init() {
	// Wire the shared package ExitFunc so that Sanitise calls use the same func.
	// The slack package tests override exitFunc; we need to propagate that override
	// into the shared package. We do this by shadowing Sanitise calls through wrappers.
	//
	// Actually, the shared package's Sanitise calls intermediate.ExitFunc directly,
	// so we need to keep the two in sync. The simplest approach: override
	// intermediate.ExitFunc here too. But since tests override exitFunc AFTER init(),
	// we use a trampoline approach — see slackExitFuncTrampoline.
	intermediate.ExitFunc = slackExitFuncTrampoline
}

// slackExitFuncTrampoline delegates to the slack-package exitFunc so that test
// overrides of exitFunc are honoured in Sanitise calls made from this package.
func slackExitFuncTrampoline(code int) {
	exitFunc(code)
}

func (t *Transformer) TransformUsers(users []SlackUser, skipEmptyEmails bool, defaultEmailDomain string) {
	t.Logger.Info("Transforming users")

	t.Logger.Debugf("TransformUsers: Input SlackUser structs: %+v", users)

	resultUsers := map[string]*IntermediateUser{}
	for _, user := range users {
		var deleteAt int64 = 0
		if user.Deleted {
			deleteAt = model.GetMillis()
		}

		firstName := ""
		lastName := ""
		if user.Profile.RealName != "" {
			names := strings.Split(user.Profile.RealName, " ")
			firstName = names[0]
			lastName = strings.Join(names[1:], " ")
		}

		t.Logger.Debugf("TransformUsers: SlackUser struct: %+v", user)
		t.Logger.Debugf("TransformUsers: SlackUser.Profile struct: %+v", user.Profile)

		newUser := &IntermediateUser{
			Id:        user.Id,
			Username:  user.Username,
			FirstName: firstName,
			LastName:  lastName,
			Position:  user.Profile.Title,
			Email:     user.Profile.Email,
			Password:  model.NewId(),
			DeleteAt:  deleteAt,
		}

		t.Logger.Debugf("TransformUsers: newUser IntermediateUser struct: %+v", newUser)

		if user.IsBot {
			newUser.Id = user.Profile.BotID
		}

		newUser.Sanitise(t.Logger, defaultEmailDomain, skipEmptyEmails)
		resultUsers[newUser.Id] = newUser
		t.Logger.Debugf("Slack user with email %s and password %s has been imported.", newUser.Email, newUser.Password)
	}

	t.Intermediate.UsersById = resultUsers
}

func (t *Transformer) filterValidMembers(members []string, users map[string]*IntermediateUser) []string {
	validMembers := []string{}
	for _, member := range members {
		if _, ok := users[member]; ok {
			validMembers = append(validMembers, member)
		} else {
			// Create a new deleted user for this lost reference so we can handle channel memberships appropriately
			t.CreateIntermediateUser(member)
			validMembers = append(validMembers, member)
		}
	}
	return validMembers
}

func getOriginalName(channel SlackChannel) string {
	if channel.Name == "" {
		return channel.Id
	} else {
		return channel.Name
	}
}

func (t *Transformer) TransformChannels(channels []SlackChannel) []*IntermediateChannel {
	resultChannels := []*IntermediateChannel{}
	for _, channel := range channels {
		validMembers := t.filterValidMembers(channel.Members, t.Intermediate.UsersById)
		if (channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup) && len(validMembers) <= 1 {
			t.Logger.Warnf("Bulk export for direct channels containing a single member is not supported. Not importing channel %s", channel.Name)
			continue
		}

		if channel.Type == model.ChannelTypeGroup && len(validMembers) > model.ChannelGroupMaxUsers {
			channel.Name = channel.Purpose.Value
			channel.Type = model.ChannelTypePrivate
		}

		name := SlackConvertChannelName(channel.Name, channel.Id)
		newChannel := &IntermediateChannel{
			OriginalName: getOriginalName(channel),
			Name:         name,
			DisplayName:  channel.Name,
			Members:      validMembers,
			Purpose:      channel.Purpose.Value,
			Header:       channel.Topic.Value,
			Type:         channel.Type,
		}

		newChannel.Sanitise(t.Logger)
		resultChannels = append(resultChannels, newChannel)
	}

	return resultChannels
}

func (t *Transformer) PopulateUserMemberships() {
	t.Logger.Info("Populating user memberships")

	for userId, user := range t.Intermediate.UsersById {
		memberships := []string{}
		for _, channel := range t.Intermediate.PublicChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		for _, channel := range t.Intermediate.PrivateChannels {
			for _, memberId := range channel.Members {
				if userId == memberId {
					memberships = append(memberships, channel.Name)
					break
				}
			}
		}
		user.Memberships = memberships
	}
}

func (t *Transformer) PopulateChannelMemberships() {
	t.Logger.Info("Populating channel memberships")

	for _, channel := range t.Intermediate.GroupChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
	for _, channel := range t.Intermediate.DirectChannels {
		members := []string{}
		for _, memberId := range channel.Members {
			if user, ok := t.Intermediate.UsersById[memberId]; ok {
				members = append(members, user.Username)
			}
		}

		channel.MembersUsernames = members
	}
}

func (t *Transformer) TransformAllChannels(slackExport *SlackExport) error {
	t.Logger.Info("Transforming channels")

	// transform public
	t.Intermediate.PublicChannels = t.TransformChannels(slackExport.PublicChannels)

	// transform private
	t.Intermediate.PrivateChannels = t.TransformChannels(slackExport.PrivateChannels)

	// transform group
	regularGroupChannels, bigGroupChannels := SplitChannelsByMemberSize(slackExport.GroupChannels, model.ChannelGroupMaxUsers)

	t.Intermediate.PrivateChannels = append(t.Intermediate.PrivateChannels, t.TransformChannels(bigGroupChannels)...)

	t.Intermediate.GroupChannels = t.TransformChannels(regularGroupChannels)

	// transform direct
	t.Intermediate.DirectChannels = t.TransformChannels(slackExport.DirectChannels)

	return nil
}

func AddPostToThreads(original SlackPost, post *IntermediatePost, threads map[string]*IntermediatePost, channel *IntermediateChannel, timestamps map[int64]bool) {
	// direct and group posts need the channel members in the import line
	if channel.Type == model.ChannelTypeDirect || channel.Type == model.ChannelTypeGroup {
		post.IsDirect = true
		post.ChannelMembers = channel.MembersUsernames
	} else {
		post.IsDirect = false
	}

	// avoid timestamp duplications
	for {
		// if the timestamp hasn't been used already, break and use
		if _, ok := timestamps[post.CreateAt]; !ok {
			break
		}
		post.CreateAt++
	}
	timestamps[post.CreateAt] = true

	// if post is part of a thread
	if original.ThreadTS != "" && original.ThreadTS != original.TimeStamp {
		rootPost, ok := threads[original.ThreadTS]
		if !ok {
			log.Printf("ERROR processing post in thread, couldn't find rootPost: %+v\n", original)
			return
		}
		rootPost.Replies = append(rootPost.Replies, post)
		return
	}

	// if post is the root of a thread
	if original.TimeStamp == original.ThreadTS {
		if threads[original.ThreadTS] != nil {
			log.Println("WARNING: overwriting root post for thread " + original.ThreadTS)
		}
		threads[original.ThreadTS] = post
		return
	}

	if threads[original.TimeStamp] != nil {
		log.Println("WARNING: overwriting root post for thread " + original.TimeStamp)
	}

	threads[original.TimeStamp] = post
}

func buildChannelsByOriginalNameMap(inter *Intermediate) map[string]*IntermediateChannel {
	channelsByName := map[string]*IntermediateChannel{}
	for _, channel := range inter.PublicChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range inter.PrivateChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range inter.GroupChannels {
		channelsByName[channel.OriginalName] = channel
	}
	for _, channel := range inter.DirectChannels {
		channelsByName[channel.OriginalName] = channel
	}
	return channelsByName
}

func getNormalisedFilePath(file *SlackFile, attachmentsDir string) string {
	n := makeAlphaNum(file.Name, '.', '-', '_')
	p := path.Join(attachmentsDir, fmt.Sprintf("%s_%s", file.Id, n))
	return norm.NFC.String(p)
}

func addFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string, allowDownload bool) error {
	if _, ok := uploads[file.Id]; ok || !allowDownload {
		return addZipFileToPost(file, uploads, post, attachmentsDir)
	}

	return addDownloadToPost(file, post, attachmentsDir)
}

func addDownloadToPost(file *SlackFile, post *IntermediatePost, attachmentsDir string) error {
	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	fullFilePath := path.Join(attachmentsDir, destFilePath)

	log.Printf("Downloading %q into %q...\n", file.DownloadURL, destFilePath)

	err := downloadInto(fullFilePath, file.DownloadURL, file.Size)
	if err != nil {
		return err
	}

	log.Println("Download successful!")

	post.Attachments = append(post.Attachments, destFilePath)
	return nil
}

var sizes = []string{"KiB", "MiB", "GiB", "TiB", "PiB"}

func humanSize(size int64) string {
	if size < 0 {
		return "unknown"
	}
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	limit := int64(1024 * 1024)
	for _, name := range sizes {
		if size < limit {
			return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), name)
		}

		limit *= 1024
	}

	return fmt.Sprintf("%.2f %s", float64(size)/float64(limit/1024), sizes[len(sizes)-1])
}

func addZipFileToPost(file *SlackFile, uploads map[string]*zip.File, post *IntermediatePost, attachmentsDir string) error {
	zipFile, ok := uploads[file.Id]
	if !ok {
		return errors.Errorf("failed to retrieve file with id %s", file.Id)
	}

	zipFileReader, err := zipFile.Open()
	if err != nil {
		return errors.Wrapf(err, "failed to open attachment from zipfile for id %s", file.Id)
	}
	defer zipFileReader.Close()

	destFilePath := getNormalisedFilePath(file, attachmentsInternal)
	destFile, err := os.Create(path.Join(attachmentsDir, destFilePath))
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, zipFileReader)
	if err != nil {
		return errors.Wrapf(err, "failed to create file %s in the attachments directory", file.Id)
	}

	log.Printf("SUCCESS COPYING FILE %s TO DEST %s", file.Id, destFilePath)

	post.Attachments = append(post.Attachments, destFilePath)

	return nil
}

func (t *Transformer) CreateIntermediateUser(userID string) {
	newUser := &IntermediateUser{
		Id:        userID,
		Username:  strings.ToLower(userID),
		FirstName: "Deleted",
		LastName:  "User",
		Email:     fmt.Sprintf("%s@local", userID),
		Password:  model.NewId(),
	}
	t.Intermediate.UsersById[userID] = newUser
	t.Logger.Warnf("Created a new user because the original user was missing from the import files. user=%s", userID)
}

func (t *Transformer) CreateAndAddPostToThreads(post SlackPost, threads map[string]*IntermediatePost, timestamps map[int64]bool, channel *IntermediateChannel) {
	author := t.Intermediate.UsersById[post.User]
	if author == nil {
		t.CreateIntermediateUser(post.User)
		author = t.Intermediate.UsersById[post.User]
	}

	newPost := &IntermediatePost{
		User:      author.Username,
		Channel:   channel.Name,
		Message:   post.Text,
		Reactions: t.getReactionsFromPost(post),
		CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
	}

	AddPostToThreads(post, newPost, threads, channel, timestamps)
}

func (t *Transformer) AddFilesToPost(post *SlackPost, skipAttachments bool, slackExport *SlackExport, attachmentsDir string, newPost *IntermediatePost, allowDownload bool) {
	if skipAttachments || (post.File == nil && post.Files == nil) {
		return
	}
	if post.File != nil {
		if err := addFileToPost(post.File, slackExport.Uploads, newPost, attachmentsDir, allowDownload); err != nil {
			t.Logger.WithError(err).Error("Failed to add file to post")
		}
	} else if post.Files != nil {
		for _, file := range post.Files {
			if file.Name == "" {
				t.Logger.Warnf("Not able to access the file %s as file access is denied so skipping", file.Id)
				continue
			}
			if err := addFileToPost(file, slackExport.Uploads, newPost, attachmentsDir, allowDownload); err != nil {
				t.Logger.WithError(err).Error("Failed to add file to post")
			}
		}
	}
}

func (t *Transformer) AddAttachmentsToPost(post *SlackPost, newPost *IntermediatePost) (model.StringInterface, []byte) {
	props := model.StringInterface{"attachments": post.Attachments}
	propsByteArray, _ := json.Marshal(props)
	return props, propsByteArray
}

func buildMessagePropsFromHuddle(post *SlackPost) model.StringInterface {
	type Attachment struct {
		ID       int    `json:"id"`
		Text     string `json:"text"`
		Fallback string `json:"fallback"`
	}

	type MessageProps struct {
		Title       string       `json:"title"`
		EndAt       int64        `json:"end_at"`
		StartAt     int64        `json:"start_at"`
		Attachments []Attachment `json:"attachments"`
		FromPlugin  bool         `json:"from_plugin"`
	}

	props := MessageProps{
		Title: "",
		Attachments: []Attachment{{
			ID:       0,
			Text:     "Call ended",
			Fallback: "Call ended",
		}},
		FromPlugin: true,
		EndAt:      0,
		StartAt:    0,
	}

	if post.Room != nil {
		props.EndAt = post.Room.DateEnd * 1000
		props.StartAt = post.Room.DateStart * 1000
	}

	propsMap := make(map[string]any)
	bytes, _ := json.Marshal(props)
	_ = json.Unmarshal(bytes, &propsMap)

	return propsMap
}

func (t *Transformer) getReactionsFromPost(post SlackPost) []*IntermediateReaction {
	reactions := []*IntermediateReaction{}
	for _, reaction := range post.Reactions {
		for _, reactionUser := range reaction.Users {
			reactionAuthor := t.Intermediate.UsersById[reactionUser]
			if reactionAuthor == nil {
				t.CreateIntermediateUser(reactionUser)
				reactionAuthor = t.Intermediate.UsersById[reactionUser]
			}
			var cleanedReactionName = reaction.Name
			if strings.Contains(reaction.Name, "::") {
				cleanedReactionName = strings.Split(reaction.Name, "::")[0]
			}
			newReaction := &IntermediateReaction{
				User:      reactionAuthor.Username,
				EmojiName: cleanedReactionName,
				CreateAt:  SlackConvertTimeStamp(post.TimeStamp) + 1,
				// we don't have the real createAt available, so we pretend that reactions were created shortly after the post,
				// to avoid validation errors at import time:
				// BulkImport: Reaction CreateAt property must be greater than the parent post CreateAt.
			}
			reactions = append(reactions, newReaction)
		}
	}
	return reactions
}

// splitPostIntoThread splits a post's message if it exceeds the maximum rune limit.
// The first chunk becomes/remains the main post, and additional chunks are added as replies.
// Reactions and attachments are kept only on the first chunk.
func splitPostIntoThread(post *IntermediatePost) {
	if utf8.RuneCountInString(post.Message) <= model.PostMessageMaxRunesV2 {
		// No splitting needed
		return
	}

	chunks := splitTextIntoChunks(post.Message, model.PostMessageMaxRunesV2)

	// First chunk stays as the main message
	post.Message = chunks[0]

	// Create replies for the remaining chunks
	for i, chunk := range chunks[1:] {
		reply := &IntermediatePost{
			User:           post.User,
			Channel:        post.Channel,
			Message:        chunk,
			CreateAt:       post.CreateAt + int64(i+1), // Increment timestamp to maintain order
			IsDirect:       post.IsDirect,
			ChannelMembers: post.ChannelMembers,
			// No reactions, attachments, or props for continuation chunks
		}
		post.Replies = append(post.Replies, reply)
	}
}

func (t *Transformer) TransformPosts(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload bool) error {
	t.Logger.Info("Transforming posts")

	newGroupChannels := []*IntermediateChannel{}
	newDirectChannels := []*IntermediateChannel{}
	channelsByOriginalName := buildChannelsByOriginalNameMap(t.Intermediate)

	resultPosts := []*IntermediatePost{}
	for originalChannelName, channelPosts := range slackExport.Posts {
		channel, ok := channelsByOriginalName[originalChannelName]
		if !ok {
			t.Logger.Warnf("--- Couldn't find channel %s referenced by posts", originalChannelName)
			continue
		}

		timestamps := make(map[int64]bool)
		sort.Slice(channelPosts, func(i, j int) bool {
			return SlackConvertTimeStamp(channelPosts[i].TimeStamp) < SlackConvertTimeStamp(channelPosts[j].TimeStamp)
		})
		threads := map[string]*IntermediatePost{}

		for _, post := range channelPosts {
			switch {
			// plain message that can have files attached
			case post.IsPlainMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}
				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}
				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, newPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, newPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if discardInvalidProps {
							t.Logger.Warn("Unable import post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add props to post as they exceed the maximum character count.")
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)

			// file comment
			case post.IsFileComment():
				if post.Comment == nil {
					t.Logger.Warn("Unable to import the message as it has no comments.")
					continue
				}
				if post.Comment.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				author := t.Intermediate.UsersById[post.Comment.User]
				if author == nil {
					t.CreateIntermediateUser(post.User)
					author = t.Intermediate.UsersById[post.User]
				}
				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Comment.Comment,
					Reactions: t.getReactionsFromPost(post),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)

			// bot message
			case post.IsBotMessage():
				if post.BotId == "" {
					if post.User == "" {
						t.Logger.Warn("Unable to import the message as the user field is missing.")
						continue
					}
					post.BotId = post.User
				}

				author := t.Intermediate.UsersById[post.BotId]
				if author == nil {
					t.CreateIntermediateUser(post.BotId)
					author = t.Intermediate.UsersById[post.BotId]
				}

				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
				}

				t.AddFilesToPost(&post, skipAttachments, slackExport, attachmentsDir, newPost, allowDownload)

				if len(post.Attachments) > 0 {
					props, propsB := t.AddAttachmentsToPost(&post, newPost)
					if utf8.RuneCount(propsB) <= model.PostPropsMaxRunes {
						newPost.Props = props
					} else {
						if discardInvalidProps {
							t.Logger.Warn("Unable to import the post as props exceed the maximum character count. Skipping as --discard-invalid-props is enabled.")
							continue
						} else {
							t.Logger.Warn("Unable to add the props to post as they exceed the maximum character count.")
						}
					}
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)

			// channel join/leave messages
			case post.IsJoinLeaveMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}

				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// me message
			case post.IsMeMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change topic message
			case post.IsChannelTopicMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel purpose message
			case post.IsChannelPurposeMessage():
				if post.User == "" {
					t.Logger.Warn("Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// change channel name message
			case post.IsChannelNameMessage():
				if post.User == "" {
					t.Logger.Warn("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}
				t.CreateAndAddPostToThreads(post, threads, timestamps, channel)

			// Huddle thread
			case post.isHuddleThread():
				post.Text = "Call ended"
				if post.User == "" {
					t.Logger.Warn("Slack Import: Unable to import the message as the user field is missing.")
					continue
				}

				// all huddles are owned by USLACKBOT, but the room has a CreatedBy prop.
				// this lets us get the actual user who created the huddle and fit with how Mattermost works.
				poster := post.User
				if post.Room != nil && len(post.Room.CreatedBy) > 0 {
					poster = post.Room.CreatedBy
				}

				author := t.Intermediate.UsersById[poster]
				if author == nil {
					t.CreateIntermediateUser(poster)
					author = t.Intermediate.UsersById[poster]
				}

				huddleProps := buildMessagePropsFromHuddle(&post)

				newPost := &IntermediatePost{
					User:      author.Username,
					Channel:   channel.Name,
					Message:   post.Text,
					Reactions: t.getReactionsFromPost(post),
					CreateAt:  SlackConvertTimeStamp(post.TimeStamp),
					Props:     huddleProps,
					Type:      "custom_calls",
				}

				AddPostToThreads(post, newPost, threads, channel, timestamps)
			default:
				t.Logger.Warnf("Unable to import the message as its type is not supported. post_type=%s, post_subtype=%s", post.Type, post.SubType)
			}
		}

		channelPosts := []*IntermediatePost{}
		for _, post := range threads {
			// Split the post if it exceeds the maximum rune limit
			splitPostIntoThread(post)

			// Also split any existing replies that exceed the limit
			// We need to iterate carefully because we'll be modifying the replies slice
			originalReplies := post.Replies
			post.Replies = []*IntermediatePost{}

			// Build a set of used timestamps from existing replies to avoid duplicates
			usedTimestamps := make(map[int64]bool)
			for _, reply := range originalReplies {
				usedTimestamps[reply.CreateAt] = true
			}

			for _, reply := range originalReplies {
				if utf8.RuneCountInString(reply.Message) <= model.PostMessageMaxRunesV2 {
					// Reply doesn't need splitting, keep as-is
					post.Replies = append(post.Replies, reply)
					continue
				}

				// Reply needs splitting - add all chunks as siblings
				chunks := splitTextIntoChunks(reply.Message, model.PostMessageMaxRunesV2)

				// First chunk: update the original reply
				reply.Message = chunks[0]
				post.Replies = append(post.Replies, reply)

				// Remaining chunks: create new sibling replies
				for i, chunk := range chunks[1:] {
					// Find a unique timestamp by incrementing until we find one not in use
					timestamp := reply.CreateAt + int64(i+1)
					for usedTimestamps[timestamp] {
						timestamp++
					}
					usedTimestamps[timestamp] = true

					continuationReply := &IntermediatePost{
						User:           reply.User,
						Channel:        reply.Channel,
						Message:        chunk,
						CreateAt:       timestamp,
						IsDirect:       reply.IsDirect,
						ChannelMembers: reply.ChannelMembers,
						// No reactions, attachments, or props for continuation chunks
					}
					post.Replies = append(post.Replies, continuationReply)
				}
			}

			// Sort replies by CreateAt to ensure proper ordering
			// This is important because split chunks may have timestamps that need to be
			// interleaved with other replies
			sort.Slice(post.Replies, func(i, j int) bool {
				return post.Replies[i].CreateAt < post.Replies[j].CreateAt
			})

			channelPosts = append(channelPosts, post)
		}
		resultPosts = append(resultPosts, channelPosts...)
	}

	t.Intermediate.Posts = resultPosts
	t.Intermediate.GroupChannels = append(t.Intermediate.GroupChannels, newGroupChannels...)
	t.Intermediate.DirectChannels = append(t.Intermediate.DirectChannels, newDirectChannels...)

	return nil
}

func (t *Transformer) Transform(slackExport *SlackExport, attachmentsDir string, skipAttachments, discardInvalidProps, allowDownload, skipEmptyEmails bool, defaultEmailDomain string) error {
	t.TransformUsers(slackExport.Users, skipEmptyEmails, defaultEmailDomain)

	if err := t.TransformAllChannels(slackExport); err != nil {
		return err
	}

	t.PopulateUserMemberships()
	t.PopulateChannelMemberships()

	if err := t.TransformPosts(slackExport, attachmentsDir, skipAttachments, discardInvalidProps, allowDownload); err != nil {
		return err
	}

	return nil
}

func makeAlphaNum(str string, allowAdditional ...rune) string {
	for match, replace := range specialReplacements {
		str = strings.ReplaceAll(str, match, replace)
	}

	str = norm.NFKD.String(str)
	str = strings.Map(func(r rune) rune {
		for _, allowed := range allowAdditional {
			if r == allowed {
				return r
			}
		}

		// filter all non-ASCII runes
		if r > 127 {
			return -1
		}

		// restrict the remaining characters
		if r >= 'a' && r <= 'z' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r
		}
		if r >= '0' && r <= '9' {
			return r
		}

		return '_'
	}, str)
	return str
}

var specialReplacements = map[string]string{
	"ß": "ss",
}
