# express

To install dependencies:

```bash
npm ci
```

To run:

```bash
npm start
```

The app runs with Node.js 24.

Before building the image with Containia, run `npm ci` in this directory so
`node_modules` is included by the Dockerfile's `COPY` step.
