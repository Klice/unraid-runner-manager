package names

import (
	"errors"
	"math/rand/v2"
	"strings"
)

var adjectives = []string{
	"agile", "brave", "bouncy", "breezy", "bubbly", "calm", "cheeky", "chilly", "clever", "cosmic",
	"cozy", "crafty", "crispy", "curious", "dapper", "dizzy", "dreamy", "eager", "fancy", "feisty",
	"fluffy", "frosty", "fuzzy", "gentle", "giddy", "glossy", "grumpy", "happy", "hasty", "hungry",
	"jolly", "jumpy", "lazy", "little", "lucky", "merry", "mighty", "misty", "nimble", "noisy",
	"peppy", "perky", "plucky", "polite", "proud", "quick", "quiet", "rusty", "sassy", "shiny",
	"silly", "sleepy", "sneaky", "snug", "soggy", "spicy", "spooky", "spry", "squeaky", "sturdy",
	"sunny", "swift", "tidy", "tiny", "toasty", "vivid", "wacky", "wiggly", "witty", "zesty",
}

var nouns = []string{
	"badger", "bagel", "beaver", "biscuit", "bison", "cactus", "capybara", "cheetah", "comet", "corgi",
	"donut", "dumpling", "falcon", "ferret", "gecko", "gopher", "hedgehog", "kettle", "koala", "lemur",
	"llama", "mango", "marmot", "meerkat", "moose", "muffin", "narwhal", "noodle", "otter", "panda",
	"pancake", "parrot", "pelican", "penguin", "pickle", "pretzel", "puffin", "quokka", "raccoon", "robot",
	"rocket", "salmon", "sloth", "sparrow", "squid", "taco", "teapot", "toaster", "tomato", "toucan",
	"turnip", "waffle", "walrus", "wombat", "yak", "zebra",
}

var ErrExhausted = errors.New("could not find an unused runner name")

type Generator struct {
	rand *rand.Rand
}

func New(src rand.Source) *Generator {
	if src == nil {
		src = rand.NewPCG(rand.Uint64(), rand.Uint64())
	}
	return &Generator{rand: rand.New(src)}
}

func (g *Generator) Generate() string {
	adj := adjectives[g.rand.IntN(len(adjectives))]
	noun := nouns[g.rand.IntN(len(nouns))]
	if g.rand.IntN(4) == 0 {
		second := adjectives[g.rand.IntN(len(adjectives))]
		if second != adj {
			return strings.Join([]string{adj, second, noun}, "-")
		}
	}
	return adj + "-" + noun
}

func (g *Generator) Unique(taken func(string) bool) (string, error) {
	for range 200 {
		name := g.Generate()
		if !taken(name) {
			return name, nil
		}
	}
	return "", ErrExhausted
}

func Valid(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(name)-1:
		default:
			return false
		}
	}
	return true
}
