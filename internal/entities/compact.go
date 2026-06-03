package entities

// BotFamilyreturns the prefix used to group bots into "families".
// If the bot name is at least 13 characters long, the first 10 are used;
// otherwise the first len(name)-3 characters are used (minimum 0).
func BotFamily(botName string) string {
	n := len(botName)
	switch {
	case n >= 13:
		return botName[:10]
	case n > 3:
		return botName[:n-3]
	default:
		return botName
	}
}

// CompactPacks removes duplicate packs that share the same filename, size
// and bot family prefix, keeping only the first occurrence of each group.
func CompactPacks(packs []*XDCCPack) []*XDCCPack {
	type key struct {
		filename  string
		size      int64
		botFamily string
	}
	seen := make(map[key]bool)
	var out []*XDCCPack
	for _, p := range packs {
		k := key{
			filename:  p.Filename,
			size:      p.Size,
			botFamily: BotFamily(p.Bot),
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}
