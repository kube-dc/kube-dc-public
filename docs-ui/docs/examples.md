---
title: Examples
description: Examples and use cases
---

# Examples

Practical examples of using the documentation website.

## Basic Example

Here's a simple markdown document:

```markdown
---
title: My Document
description: A sample document
---

# My Document

This is a paragraph with **bold** and *italic* text.

## Section

- Item 1
- Item 2
- Item 3
```

## Code Examples

### JavaScript

```javascript
const greeting = (name) => {
  return `Hello, ${name}!`;
};

console.log(greeting('World'));
```

### Python

```python
def fibonacci(n):
    if n <= 1:
        return n
    return fibonacci(n-1) + fibonacci(n-2)

print(fibonacci(10))
```

### JSON

```json
{
  "name": "Documentation Site",
  "version": "1.0.0",
  "framework": "PatternFly v5"
}
```

## Tables Example

### Feature Comparison

| Feature | Basic | Pro | Enterprise |
|---------|-------|-----|------------|
| Markdown Support | ✓ | ✓ | ✓ |
| Custom Themes | - | ✓ | ✓ |
| API Access | - | - | ✓ |
| Support | Email | Priority | 24/7 |

## Lists

### Ordered List

1. First item
2. Second item
3. Third item
   1. Nested item
   2. Another nested item

### Unordered List

- Main point
  - Sub point
  - Another sub point
- Another main point

### Task List

- [x] Completed task
- [ ] Pending task
- [ ] Another pending task

## Blockquotes

> This is a blockquote. It can contain multiple paragraphs.
>
> Like this one.

## Links and Images

### External Link

Visit [PatternFly](https://www.patternfly.org/) for more information.

### Internal Link

Check out the [Getting Started](/docs/getting-started) guide.

## Inline Elements

This paragraph contains `inline code`, **bold text**, *italic text*, and ~~strikethrough~~.

## Horizontal Rule

---

Content after the horizontal rule.
