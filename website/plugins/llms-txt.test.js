// Run with: node --test plugins/
const test = require('node:test');
const assert = require('node:assert');
const path = require('path');
const {routeFor, targetFor} = require('./llms-txt.js');

const docs = '/site/docs';

test('routes without slug follow the file path', () => {
  assert.equal(routeFor(docs, '/site/docs/guide/deploy.md', {}), '/guide/deploy');
  assert.equal(routeFor(docs, '/site/docs/index.md', {}), '');
  assert.equal(routeFor(docs, '/site/docs/guide/index.mdx', {}), '/guide');
});

test('absolute slug is used as is', () => {
  assert.equal(routeFor(docs, '/site/docs/index.md', {slug: '/overview'}), '/overview');
});

test('relative slug resolves against the document directory', () => {
  assert.equal(routeFor(docs, '/site/docs/guide/deploy.md', {slug: 'install'}), '/guide/install');
  assert.equal(routeFor(docs, '/site/docs/guide/deploy.md', {slug: './../published'}), '/published');
});

test('a slug that climbs out of the docs root is clamped and cannot escape the build dir', () => {
  const route = routeFor(docs, '/site/docs/guide/deploy.md', {slug: '../../../../etc/cron.d/x'});
  assert.equal(route, '/etc/cron.d/x');
  assert.ok(targetFor('/site/build', route).startsWith('/site/build/'));
  assert.throws(() => targetFor('/site/build', '/../outside'), /refusing to write outside/);
});
