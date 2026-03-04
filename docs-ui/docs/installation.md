---
title: Installation
description: How to install and set up the project
---

# Installation

This guide will help you set up the documentation website.

## Prerequisites

Before you begin, ensure you have the following installed:

- **Node.js** (version 16 or higher)
- **npm** or **yarn**

## Installation Steps

### 1. Install Dependencies

```bash
npm install
```

Or if you're using yarn:

```bash
yarn install
```

### 2. Start Development Server

```bash
npm start
```

The application will start on `http://localhost:3000`.

### 3. Build for Production

```bash
npm run build
```

This creates an optimized production build in the `build` folder.

## Project Structure

```
docs/
├── public/
│   ├── docs/           # Markdown documentation files
│   └── index.html
├── src/
│   ├── components/     # React components
│   ├── utils/          # Utility functions
│   ├── App.js
│   └── index.js
└── package.json
```

## Adding New Documentation

To add new documentation pages:

1. Create a new `.md` file in `public/docs/`
2. Add frontmatter with title and description
3. Update `src/utils/docStructure.js` to include the new page in navigation

Example:

```markdown
---
title: My New Page
description: Description of the page
---

# My New Page

Content goes here...
```
