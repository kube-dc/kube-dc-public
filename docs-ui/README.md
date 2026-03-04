# Kube-DC Documentation

This documentation site is built using [Docusaurus](https://docusaurus.io/), a modern static website generator.

## Prerequisites

- Node.js version 18.0 or above
- npm (comes with Node.js)

## Installation

Install dependencies:

```bash
npm install
```

## Local Development

Start the development server:

```bash
npm run start
```

This command starts a local development server at `http://localhost:3000` and opens it in your browser. Most changes are reflected live without having to restart the server.

## Build

Generate static content for production:

```bash
npm run build
```

This command generates static content into the `build` directory that can be served using any static hosting service.

## Serve Production Build Locally

To test the production build locally:

```bash
npm run serve
```

This serves the `build` directory at `http://localhost:3000`.

## Features

- **Search**: Full-text search across all documentation (Ctrl+K / Cmd+K)
- **Code Highlighting**: Syntax highlighting for multiple languages
- **Mermaid Diagrams**: Support for flowcharts and diagrams
- **PatternFly Styling**: Custom theme matching PatternFly design system
- **Responsive**: Mobile-friendly navigation and layout

## Project Structure

```
docs-ui/
├── docs/              # Documentation markdown files
├── static/            # Static assets (images, etc.)
├── src/
│   ├── css/          # Custom CSS
│   └── pages/        # Custom pages
├── docusaurus.config.ts  # Docusaurus configuration
└── sidebars.ts       # Sidebar navigation structure
```
