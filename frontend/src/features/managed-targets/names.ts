// A new managed server starts with a three-word name, adjective-adjective-noun (brisk-amber-otter):
// easy to say and tell apart, and always a DNS label, since the name becomes Kubernetes object names.

const ADJECTIVES = [
  "amber",
  "bold",
  "brisk",
  "calm",
  "clever",
  "cosmic",
  "crisp",
  "dapper",
  "eager",
  "fancy",
  "gentle",
  "golden",
  "happy",
  "hazel",
  "humble",
  "jolly",
  "keen",
  "lively",
  "lucky",
  "mellow",
  "misty",
  "nimble",
  "noble",
  "plucky",
  "proud",
  "quick",
  "quiet",
  "rapid",
  "rosy",
  "rustic",
  "shiny",
  "silent",
  "silver",
  "snowy",
  "spry",
  "steady",
  "sunny",
  "swift",
  "tidy",
  "witty",
] as const;

const NOUNS = [
  "badger",
  "beacon",
  "bison",
  "breeze",
  "canyon",
  "cedar",
  "comet",
  "coral",
  "falcon",
  "fern",
  "finch",
  "fjord",
  "gecko",
  "glacier",
  "harbor",
  "heron",
  "island",
  "lagoon",
  "lantern",
  "lynx",
  "maple",
  "meadow",
  "meteor",
  "otter",
  "panda",
  "pebble",
  "pine",
  "quartz",
  "raven",
  "reef",
  "river",
  "robin",
  "spruce",
  "summit",
  "thistle",
  "tundra",
  "walrus",
  "willow",
  "wren",
  "zephyr",
] as const;

// Tries before a taken name gets a number: with 62,400 names, collisions are rare.
const ATTEMPTS = 20;

function pick<T>(words: readonly T[], random: () => number): T {
  // random() is in [0, 1), so the index is always in range.
  return words[Math.floor(random() * words.length)] as T;
}

/** A three-word name no existing managed server uses. */
export function generateName(taken: Iterable<string> = [], random: () => number = Math.random): string {
  const used = new Set(taken);
  let name = "";
  for (let attempt = 0; attempt < ATTEMPTS; attempt++) {
    const first = pick(ADJECTIVES, random);
    const second = pick(
      ADJECTIVES.filter((word) => word !== first),
      random,
    );
    name = `${first}-${second}-${pick(NOUNS, random)}`;
    if (!used.has(name)) return name;
  }
  for (let suffix = 2; ; suffix++) if (!used.has(`${name}-${suffix}`)) return `${name}-${suffix}`;
}

/** The words, for tests. */
export const NAME_WORDS = { adjectives: ADJECTIVES, nouns: NOUNS };
