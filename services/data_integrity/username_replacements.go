package data_integrity

import (
	"github.com/mattermost/mattermost/server/v8/channels/app/imports"
)

func processPost(post *imports.PostImportData, usernameMappings map[string]string) *imports.PostImportData {
	post.FlaggedBy = replaceUsernames(post.FlaggedBy, usernameMappings)
	post.User = replaceUsername(post.User, usernameMappings)

	post.Reactions = handleReactions(post.Reactions, usernameMappings)
	post.Replies = handleReplies(post.Replies, usernameMappings)

	return post
}

func processDirectPost(directPost *imports.DirectPostImportData, usernameMappings map[string]string) *imports.DirectPostImportData {
	directPost.ChannelMembers = replaceUsernames(directPost.ChannelMembers, usernameMappings)
	directPost.FlaggedBy = replaceUsernames(directPost.FlaggedBy, usernameMappings)
	directPost.User = replaceUsername(directPost.User, usernameMappings)

	directPost.Reactions = handleReactions(directPost.Reactions, usernameMappings)
	directPost.Replies = handleReplies(directPost.Replies, usernameMappings)

	return directPost
}

func processChannel(channel *imports.ChannelImportData, usernameMappings map[string]string) *imports.ChannelImportData {
	return channel
}

func processDirectChannel(directChannel *imports.DirectChannelImportData, usernameMappings map[string]string) *imports.DirectChannelImportData {
	directChannel.FavoritedBy = replaceUsernames(directChannel.FavoritedBy, usernameMappings)
	directChannel.Members = replaceUsernames(directChannel.Members, usernameMappings)
	return directChannel
}

func handleReplies(replies *[]imports.ReplyImportData, usernameMappings map[string]string) *[]imports.ReplyImportData {
	if replies == nil {
		return replies
	}

	outReplies := []imports.ReplyImportData{}

	for _, reply := range *replies {
		reply.FlaggedBy = replaceUsernames(reply.FlaggedBy, usernameMappings)
		reply.User = replaceUsername(reply.User, usernameMappings)
		reply.Reactions = handleReactions(reply.Reactions, usernameMappings)
		outReplies = append(outReplies, reply)
	}

	return &outReplies
}

func handleReactions(reactions *[]imports.ReactionImportData, usernameMappings map[string]string) *[]imports.ReactionImportData {
	if reactions == nil {
		return reactions
	}

	outReactions := []imports.ReactionImportData{}
	for _, reaction := range *reactions {
		reaction.User = replaceUsername(reaction.User, usernameMappings)
		outReactions = append(outReactions, reaction)
	}

	return &outReactions
}

func replaceUsername(input *string, usernameMappings map[string]string) *string {
	if input == nil {
		return input
	}

	if newUsername, ok := usernameMappings[*input]; ok {
		return &newUsername
	}

	return input
}

func replaceUsernames(input *[]string, usernameMappings map[string]string) *[]string {
	if input == nil {
		return input
	}

	newUsers := []string{}
	for _, u := range *input {
		if newUsername, ok := usernameMappings[u]; ok {
			newUsers = append(newUsers, newUsername)
		} else {
			newUsers = append(newUsers, u)
		}
	}

	return &newUsers
}
