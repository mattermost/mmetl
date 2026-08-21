package intermediate

// PruneAttachments removes any attachment path in failed from every post and
// reply in inter, in place. Sources that extract attachments as a separate
// pass after building Intermediate (e.g. RocketChat's ExtractAttachments) need
// this: a path can already be embedded in a post's Attachments before
// extraction is attempted, and extraction can fail for some files (missing
// source, unsafe path, I/O error) independently of that. Without pruning, the
// exported JSONL would reference a file that was never written to disk, and
// BuildCounts would double-count the same attachment as both Transformed
// (still present here) and Skipped (from the extraction-failure log).
func PruneAttachments(inter *Intermediate, failed map[string]bool) {
	if len(failed) == 0 {
		return
	}

	prune := func(p *IntermediatePost) {
		if len(p.Attachments) == 0 {
			return
		}
		kept := p.Attachments[:0]
		for _, path := range p.Attachments {
			if !failed[path] {
				kept = append(kept, path)
			}
		}
		p.Attachments = kept
	}

	for _, post := range inter.Posts {
		prune(post)
		for _, reply := range post.Replies {
			prune(reply)
		}
	}
}
