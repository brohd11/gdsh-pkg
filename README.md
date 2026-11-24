# gdsh-pkg

Installs gdsh command packages into a command root (`<root>/packages/<name>`) from the reviewed
index [godot-gdsh-lib-manifest](https://github.com/brohd11/godot-gdsh-lib-manifest). It is
shared by the gdsh CLI (`gdsh package install`) and mcp-sh-godot
(`mcp_sh_godot gdsh package install`). It has no dependencies outside the standard library.

```go
c := &gdshpkg.Client{}                   // published index, cached for a day
idx, _ := c.FetchIndex(ctx, false)
steps, _ := idx.Plan(root, []string{"terrain3d"}, false) // level first, as a sibling
err := c.Install(ctx, root, steps, func(line string) { fmt.Println(line) })
```

- Installs download the commit the index pins (`archive/<commit>.zip`), not the tag, so a moved
  tag can't change what is served.
- The archive's wrapper folder, dot files and `_export_ignore/` are dropped. The package is
  unpacked under `<root>/.pkg-staging` and renamed into place, so a failed install leaves the
  old copy untouched.
- A package with non-gdsh requires (`project_only`) is refused, since a command root can't hold
  that code. Install it into the project with gdaddon.
- `GDSH_PACKAGE_INDEX` overrides the index: a URL, or a local `manifest.json`.

`cmd/gdsh-manifest` (`check`, `pin`, `propose`) maintains the index; see its `--help` and the
manifest repo's README.
