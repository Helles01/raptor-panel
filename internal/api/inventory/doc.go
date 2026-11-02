// Package inventory holds the test that registers every endpoint package on one router and compares the routes with the reference
// panel's. It has no code of its own: it exists so that a route that is forgotten, added by accident or opened to the public shows up as
// a failing test and not in production.
package inventory
