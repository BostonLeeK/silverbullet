import { expect, test } from "vitest";
import {
  isSpaceShippedPath,
  resolveMarkdownLink,
} from "@silverbulletmd/silverbullet/lib/resolve";

test("isSpaceShippedPath", () => {
  expect(isSpaceShippedPath("Library/Std/Foo")).toEqual(true);
  expect(isSpaceShippedPath("Repositories/Std")).toEqual(true);
  expect(isSpaceShippedPath("index")).toEqual(false);
  expect(isSpaceShippedPath("My Library/note")).toEqual(false);
});

test("Test URL resolver", () => {
  // Absolute paths
  expect(resolveMarkdownLink("foo", "/bar")).toEqual("bar");
  expect(resolveMarkdownLink("/foo/bar/baz", "/qux")).toEqual("qux");
  expect(resolveMarkdownLink("foo", "/bar@123#456")).toEqual("bar@123#456");
  expect(resolveMarkdownLink("foo/bar", "/baz.jpg")).toEqual("baz.jpg");

  // Relative paths
  expect(resolveMarkdownLink("bar", "foo")).toEqual("foo");
  expect(resolveMarkdownLink("foo/bar.jpg", "baz")).toEqual("foo/baz");
  expect(resolveMarkdownLink("/foo/bar", "baz")).toEqual("/foo/baz");
  expect(resolveMarkdownLink("foo///bar", "baz")).toEqual("foo///baz");
  expect(resolveMarkdownLink("bar", "../foo/baz")).toEqual("foo/baz");
  expect(resolveMarkdownLink("bar", "../../foo/baz")).toEqual("foo/baz");
  expect(resolveMarkdownLink("bar/qux", "foo/../baz")).toEqual(
    "bar/foo/../baz",
  );
});
