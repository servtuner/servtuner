import { describe, expect, it } from "vitest";
import { NAME_WORDS, generateName } from "./names";

// The api's DNS label rule for a managed target's name.
const DNS_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;

describe("generated names", () => {
  it("are three words, two different adjectives and a noun, and always a DNS label", () => {
    for (let i = 0; i < 500; i++) {
      const name = generateName();
      const [first, second, noun] = name.split("-");
      expect(name).toMatch(DNS_LABEL);
      expect(NAME_WORDS.adjectives).toContain(first);
      expect(NAME_WORDS.adjectives).toContain(second);
      expect(first).not.toBe(second);
      expect(NAME_WORDS.nouns).toContain(noun);
    }
  });

  it("never repeat a name already taken", () => {
    const always = () => 0; // the same words every time
    const first = generateName([], always);
    const second = generateName([first], always);
    expect(second).not.toBe(first);
    expect(second).toMatch(DNS_LABEL);
    expect(generateName([first, second], always)).not.toMatch(new RegExp(`^(${first}|${second})$`));
  });

  it("keep every word a lowercase DNS fragment", () => {
    for (const word of [...NAME_WORDS.adjectives, ...NAME_WORDS.nouns]) expect(word).toMatch(/^[a-z]+$/);
  });
});
