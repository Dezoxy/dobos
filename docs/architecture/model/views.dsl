// Each view answers one question. Keep container views under ~15 elements;
// split a view before trying to style it into readability.

systemContext dobos "SystemContext" "What does Dobos depend on, and who depends on it?" {
    include *
    include viewer
    autoLayout lr
}

container dobos "Containers" "What runs, and which stores does it own?" {
    include *
    autoLayout lr
}

component dobos.daemon "Daemon" "What are the parts of the single process, and how does a read reach the swarm?" {
    include *
    autoLayout tb 300 150
}
