type MarkdownNode = {
  type: string;
  value?: string;
  name?: string;
  attributes?: Array<{name?: string}>;
};

type MarkdownRoot = {
  children: MarkdownNode[];
};

function isGithubOnly(node: MarkdownNode): boolean {
  return node.name === 'details'
    && (node.attributes ?? []).some(
      (attribute) => attribute.name === 'data-github-only',
    );
}

/**
 * Remove a GitHub-only <details data-github-only> tree before Docusaurus
 * processes its Mermaid or ASCII children. GitHub renders the disclosure and
 * its Markdown; the website never includes that subtree in its output.
 */
export default function stripGithubOnly() {
  return (tree: MarkdownRoot): void => {
    tree.children = tree.children.filter((node) => !isGithubOnly(node));
  };
}
