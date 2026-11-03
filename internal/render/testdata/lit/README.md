`lit.js` is Lit 3.3.3 (lit 3.3.3, lit-html 3.3.3, lit-element 4.2.2, @lit/reactive-element 2.1.2, all BSD-3-Clause, Copyright Google LLC; see LICENSE), bundled for the web-component capture test:

    npm install lit@3.3.3 esbuild@0.25
    echo 'export {LitElement, html, css} from "lit";' > entry.js
    npx esbuild entry.js --bundle --format=esm --minify --legal-comments=inline --outfile=lit.js

It is test data only and is not part of the RenderCache executable.
