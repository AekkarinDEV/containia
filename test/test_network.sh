#!/bin/sh
set -e
cat << 'EOF' > /tmp/test_fetch.js
try {
  const res = await fetch("https://registry.npmjs.org/");
  console.log("Fetch Status:", res.status);
} catch (err) {
  console.error("Fetch Error:", err);
}
EOF

./containia run --rm -v /tmp/test_fetch.js:/test.js oven/bun:latest bun /test.js
