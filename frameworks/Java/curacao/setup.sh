#!/bin/bash

# load java environment variables
source $IROOT/java.installed

${IROOT}/sbt/bin/sbt assembly

java -jar dist/curacao-standalone.jar &