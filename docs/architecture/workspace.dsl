// Entry point for the architecture model. Keep this file at docs/architecture/:
// !docs and !adrs paths must be this directory or a subdirectory of it.
// Model fragments live in model/ and are pulled in with !include (order matters).
workspace "Flodni" "Architecture model for Flodni, a torrent engine behind a FUSE virtual filesystem." {

    !identifiers hierarchical

    configuration {
        scope softwaresystem
    }

    // Attached to the workspace, not the software system, so both paths resolve
    // unambiguously against this file.
    !docs overview
    !adrs decisions

    properties {
        // Docs and ADRs are attached at workspace level (above), so the
        // per-system documentation/decision inspections do not apply here.
        "structurizr.inspection.model.softwaresystem.documentation" "ignore"
        "structurizr.inspection.model.softwaresystem.decisions" "ignore"
    }

    model {
        !include model/people-systems.dsl
        !include model/containers.dsl
    }

    views {
        !include model/views.dsl
        !include model/styles.dsl
    }

}
