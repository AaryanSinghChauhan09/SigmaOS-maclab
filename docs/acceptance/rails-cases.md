# The old site's 224 test cases, and what holds each now

The Rails site in `site/` came with 224 test cases. Each is held here by the Go test that replaced it, or dropped by a line of the spec. `TestEveryRailsCaseIsHeldOrDropped` in `internal/repotest` reads this table: it fails when a case of `site/test` is missing from it or listed twice, when a Go test it names is not in the package it names, and when a dropped case names no section of the spec. Once `site/` is deleted the table is the record of what its tests asked.

Two more test files in `site/test/integration` are not among the 224: `report_parity_test.rb` and `page_parity_test.rb` were written with the port, to make Rails judge, count and render the corpus that the Go tests `TestVerdictsAreTheOldSites`, `TestTheCountsAreTheOldSites`, `TestThePagesPinWhatTheOldSitesPin` and `TestTheExportsAreTheOldSites` compare the port with. They leave with `site/`.

Where a case reads "tester", "admin" or "allowlist", the lab says Verified run or M team member, Maintainer, and the M team mark on People. A test named with its package (`internal/reports:TestName`) sits outside the package the row names first.

224 cases: 216 held by a Go test, 8 dropped.

## admin_test.rb

`site/test/integration/admin_test.rb`, 18 cases.

| Case | Held by |
|---|---|
| without GitHub sign-in or ADMIN_TOKEN there is no admin | `internal/server` `TestThereIsNoSignInWithoutTheOAuthApp`, `TestALabWithNoSignInAnswersNotFoundForItsLabPages` |
| sign-in goes to GitHub with only the client ID and a one-time state, and back to the callback | `internal/server` `TestSignInIsALinkWhoseStateWorksOnce` |
| an admin handle signs in; the callback's redirect_uri matches the authorize one | `internal/server` `TestSignInMakesAPersonByAccountID`, `TestAnAdminIsWhoeverTheConfigurationNamesNow` |
| a GitHub account that isn't an admin is refused | `internal/server` `TestAPageARoleMayNotOpenIsNotFoundAndSignedOutLeadsToSignIn` |
| a callback with the wrong state, no state or a bad code signs nobody in | `internal/server` `TestSignInIsALinkWhoseStateWorksOnce`, `TestGitHubsRefusalSignsNobodyIn` |
| the state works once | `internal/server` `TestSignInIsALinkWhoseStateWorksOnce` |
| with GitHub sign-in set up, ADMIN_TOKEN no longer signs in | Dropped. The old admin session: there is no separate site admin and no standing admin token (spec 3 "Kinds of people", 5 "Credentials"). |
| taking a handle out of ADMIN_GITHUB_LOGINS signs it out | `internal/server` `TestAnAdminIsWhoeverTheConfigurationNamesNow` |
| the admin lists every report and can hide one: it leaves the site and the matrix | `internal/server` `TestAMaintainerListsEveryReportAndHidesOneWhichLeavesThePublicPagesAndTheMatrix`, `internal/reports:TestHidingTakesAReportOffWhatIsPublishedAndShowingPutsItBack` |
| the admin can show a hidden report again | `internal/server` `TestAMaintainerShowsAHiddenReportAgain` |
| the admin can delete any report | `internal/server` `TestAMaintainerDeletesAnyReportAskedTwice` |
| nobody else can hide or delete | `internal/server` `TestNobodyButAMaintainerHidesShowsOrDeletes` |
| an admin session expires | `internal/server` `TestASessionEndsAfter30DaysUnused` |
| without ADMIN_TOKEN there is no admin | Dropped. The old admin session: there is no separate site admin and no standing admin token (spec 3 "Kinds of people", 5 "Credentials"). |
| the token signs in | Dropped. The old admin session: there is no separate site admin and no standing admin token (spec 3 "Kinds of people", 5 "Credentials"). |
| the admin page asks for the token, and a wrong one is refused | Dropped. The old admin session: there is no separate site admin and no standing admin token (spec 3 "Kinds of people", 5 "Credentials"). |
| sign-in attempts are rate-limited | Dropped. The old admin session: its token form is gone; refused credentials are counted by the lab's own limiter (spec 6 "What labd does itself"). |
| changing ADMIN_TOKEN signs the admin out | Dropped. The old admin session: there is no separate site admin and no standing admin token (spec 3 "Kinds of people", 5 "Credentials"). |

## admin_testers_test.rb

`site/test/integration/admin_testers_test.rb`, 4 cases.

| Case | Held by |
|---|---|
| only the admin sees or changes the allowlist | `internal/server` `TestPeopleIsNotFoundBelowMaintainer` |
| the admin adds a handle, and that machine's runs become tester runs | `internal/server` `TestMarkingAHandleLooksItUpOnGitHubAndPinsTheAccount`, `TestASignInFromOutsideTheMTeamStaysAndPeopleShowsItNotCounted`, `TestHiddenReportsAndWhoUploadedEachShowToMaintainersOnly`, `internal/reports:TestASignInByAPersonOutsideTheMTeamStaysAndItsRunsAreCommunityRuns` |
| a handle that isn't a GitHub handle, or is already there, is refused | `internal/server` `TestMarkingAHandleLooksItUpOnGitHubAndPinsTheAccount` |
| the admin removes a tester and can unbind a machine | `internal/server` `TestThePersonAndAMaintainerEachEndAMacSignIn`, `TestARunIsVerifiedOnlyWhenItsMacIsSignedInAsAnMTeamMemberAtUpload`, `TestLeavingTheMTeam`, `internal/reports:TestAReportCarriesWhoItsMacWasSignedInAs` |

## answered_by_default_test.rb

`site/test/integration/answered_by_default_test.rb`, 6 cases.

| Case | Held by |
|---|---|
| the report is accepted and its check shows as works (unconfirmed) | `internal/server` `TestTheReportPageExplainsEachResultInPlainWords` |
| Enter-only yeses from two machines don't colour the matrix; typed ones do | `internal/server` `TestAnEnterOnlyYesColoursNoCell`, `internal/reports:TestAnswersByDefaultColourNothing` |
| a tester's Enter-only yes isn't tester-verified; a typed yes is | `internal/server` `TestAnEnterOnlyYesColoursNoCell`, `internal/reports:TestAnswersByDefaultColourNothing` |
| a candidate set whose tester runs counted nothing is still waiting, not ready | `internal/server` `TestACandidateSetWhoseVerifiedRunsCountedNothingIsStillWaiting` |
| a report without the field counts as before, and checks.csv says which answers were the default | `internal/server` `TestChecksCSVSaysWhichAnswersWereTheDefault` |
| the schema only allows true | `internal/reports` `TestAnsweredByDefaultCanOnlyBeTrue`, `TestVerdictsAreTheOldSites` |

## aurora_support_test.rb

`site/test/integration/aurora_support_test.rb`, 5 cases.

| Case | Held by |
|---|---|
| community runs don't make the table | `internal/server` `TestCommunityRunsDoNotMakeTheAuroraTable` |
| a tester's run on Aurora fills its chip generation, next to the expected states, with its sources | `internal/server` `TestAVerifiedRunOnAuroraFillsItsChipGenerationNextToTheExpectedStates` |
| runs off the Aurora kernel don't count | `internal/server` `TestAVerifiedRunOnAuroraFillsItsChipGenerationNextToTheExpectedStates` |
| each tester machine counts once with its latest run; testers who disagree make it partial | `internal/server` `TestEachVerifiedMachineCountsOnceOnAuroraAndDisagreementMakesItPartial` |
| aurora.json has the table per chip, each cell's sources and the catalogue's sources | `internal/server` `TestAuroraJSONHasTheTablePerChipEachCellsSourcesAndTheCataloguesSources`, `TestTheDataPageLinksEveryExport` |

## benchmarks_test.rb

`site/test/integration/benchmarks_test.rb`, 16 cases.

| Case | Held by |
|---|---|
| storage rows split by capacity | `internal/server` `TestStorageRowsSplitByCapacityAndOtherBenchmarksDoNot` |
| a report without a capacity is accepted and shows as unknown | `internal/server` `TestStorageRowsSplitByCapacityAndOtherBenchmarksDoNot` |
| other benchmarks don't split by capacity | `internal/server` `TestStorageRowsSplitByCapacityAndOtherBenchmarksDoNot` |
| the macOS column shows the same board's number with its conditions | `internal/server` `TestTheMacOSColumnShowsTheSameBoardsNumberWithItsConditions`, `internal/reports:TestTheMacOSReference` |
| GL and Vulkan have no macOS column | `internal/server` `TestTheMacOSColumnShowsTheSameBoardsNumberWithItsConditions` |
| storage shows macOS only at the same capacity | `internal/server` `TestTheMacOSColumnShowsTheSameBoardsNumberWithItsConditions` |
| a board with no reference shows a dash | `internal/server` `TestTheMacOSColumnShowsTheSameBoardsNumberWithItsConditions` |
| the export carries the capacity and the macOS number | `internal/server` `TestTheMacOSColumnShowsTheSameBoardsNumberWithItsConditions` |
| the run page shows the SSD's capacity, and a report without it leaves it out | `internal/server` `TestTheRunPageShowsTheSSDsCapacityAndEachScore` |
| the golden benchmark report uploads with its scores | `internal/reports` `TestTheGoldenBenchmarkReportIsStoredWithItsScores`, `TestEveryGoldenReportIsWithinTheRules` |
| each benchmark compares Macs, best first, each machine counted once with its latest score | `internal/server` `TestEachBenchmarkComparesMacsBestFirstEachMachineCountedOnceWithItsLatestScore` |
| scores from another suite are never compared with these | `internal/server` `TestEachBenchmarkComparesMacsBestFirstEachMachineCountedOnceWithItsLatestScore` |
| runs without scores, and hidden reports, add nothing | `internal/server` `TestRunsWithoutScoresAndHiddenReportsAddNothing` |
| a model page shows its scores against the best Mac, and the report shows each score | `internal/server` `TestAModelPageShowsItsScoresAgainstTheBestMac`, `TestTheRunPageShowsTheSSDsCapacityAndEachScore` |
| the comparison and every score download under CC0 | `internal/server` `TestTheComparisonAndEveryScoreDownloadUnderCC0` |
| a score outside the schema is refused | `internal/reports` `TestAScoreOutsideTheSchemaIsRefused`, `TestVerdictsAreTheOldSites` |

## build_identity_test.rb

`site/test/integration/build_identity_test.rb`, 11 cases.

| Case | Held by |
|---|---|
| every old report shows the build it was on, as the CLI derives it | `internal/server` `TestEveryOldReportShowsTheBuildItWasOnAsTheCLIDerivesIt`, `internal/reports:TestEveryOldReportIsOnTheBuildTheCLIDerives` |
| other package sets and image records are worded as the CLI words them | `internal/reports` `TestEveryOldReportIsOnTheBuildTheCLIDerives` |
| the report page header names the build, its kernel, boot package and tester, with every package | `internal/server` `TestTheReportPageHeaderNamesTheBuildItsKernelBootPackageAndTool` |
| the M3's GPU failures read as not yet supported, not as failures | `internal/server` `TestTheReportPageHeaderNamesTheBuildItsKernelBootPackageAndTool` |
| the matrix has a row per build, filters to one and can merge a release's builds | `internal/reports` `TestTheMatrixHasARowPerBuildFiltersToOneAndCanMergeAReleasesBuilds`, `internal/server:TestTheMatrixModelsAndFeaturesShowEachBuild`, `internal/server:TestTheOldMatrixLinksKeepWorkingAndKeepTheOtherFilters` |
| model and feature pages show each build | `internal/server` `TestTheMatrixModelsAndFeaturesShowEachBuild` |
| the candidates page maps each build to its candidate set when it's known | `internal/server` `TestTheCandidatesPageMapsEachBuildToItsCandidateSetWhenItIsKnown` |
| a run on an image naming its set is labelled by the set, counted for it, and shows the image record | `internal/server` `TestARunOnAnImageNamingItsSetShowsTheImageRecordAndCountsForTheSet`, `TestARunOnAnImageNamingItsSetIsOfferedByTheSetsName`, `internal/reports:TestARunOnAnImageNamingItsSetIsLabelledByTheSet` |
| two images of one set are two builds: the image's package set and build time tell them apart | `internal/server` `TestARunOnAnImageNamingItsSetShowsTheImageRecordAndCountsForTheSet`, `internal/reports:TestARunOnAnImageNamingItsSetIsLabelledByTheSet` |
| the image record takes only the builder's keys, each in its shape | `internal/reports` `TestTheImageRecordTakesOnlyTheBuildersKeysEachInItsShape` |
| exports carry the build of every report | `internal/server` `TestTheExportsCarryTheBuildOfEveryReport` |

## exports_test.rb

`site/test/integration/exports_test.rb`, 7 cases.

| Case | Held by |
|---|---|
| reports.json has every report exactly as uploaded, under CC0 | `internal/server` `TestReportsJSONHasEveryReportAsUploadedUnderCC0` |
| reports.csv has one row per report | `internal/server` `TestReportsCSVHasOneRowPerReport` |
| uploaded text that looks like a spreadsheet formula is exported as text | `internal/server` `TestReportsCSVHasOneRowPerReport`, `internal/reports:TestACSVQuotesEveryFieldAndDefusesWhatASpreadsheetWouldRun` |
| checks.csv has one row per check result | `internal/server` `TestChecksCSVHasOneRowPerCheckResult` |
| matrix.json has each cell's state and per-machine agreement | `internal/server` `TestMatrixJSONHasEachCellsStateAndPerMachineAgreement` |
| the data page links every export | `internal/server` `TestTheDataPageLinksEveryExport` |
| hidden and deleted reports are left out of every export | `internal/server` `TestHiddenAndDeletedReportsAreLeftOutOfEveryExport` |

## filters_test.rb

`site/test/integration/filters_test.rb`, 21 cases.

| Case | Held by |
|---|---|
| every run filter narrows the reports list, and the count says how many match | `internal/server` `TestEveryRunFilterNarrowsTheReportsListAndTheCountSaysHowManyMatch`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| the upload date range takes whole UTC days, each end included | `internal/server` `TestEveryRunFilterNarrowsTheReportsListAndTheCountSaysHowManyMatch`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| tester runs only, or community runs only | `internal/server` `TestVerifiedRunsOnlyOrCommunityRunsOnly`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| feature filters keep the catalogue and narrow the result boxes by model and build | `internal/server` `TestFeatureFiltersKeepTheCatalogueAndNarrowTheResultBoxesByModelAndBuild` |
| feature build choices follow the model and distinguish variants of one runtime | `internal/server` `TestFeatureFiltersKeepTheCatalogueAndNarrowTheResultBoxesByModelAndBuild`, `internal/reports:TestTheChoicesAreTheRunsOwnNewestFirst` |
| unknown feature filters show no results without hiding the catalogue | `internal/server` `TestFeatureFiltersKeepTheCatalogueAndNarrowTheResultBoxesByModelAndBuild` |
| the running kernel filter narrows pages and exports and keeps unknown choices | `internal/server` `TestTheKernelFilterNarrowsPagesAndExportsAndKeepsUnknownChoices` |
| kernel choices sort version numbers naturally and use the running kernel | `internal/server` `TestTheKernelFilterNarrowsPagesAndExportsAndKeepsUnknownChoices`, `internal/reports:TestTheChoicesAreTheRunsOwnNewestFirst` |
| filters combine with AND, show as removable chips, and clear goes back to everything | `internal/server` `TestFiltersCombineShowAsRemovableChipsAndClearGoesBackToEverything`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| a build filter as long as an image's build words is kept | `internal/server` `TestFiltersCombineShowAsRemovableChipsAndClearGoesBackToEverything`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| an unknown value matches nothing, and a value past the choices still shows as picked | `internal/server` `TestFiltersCombineShowAsRemovableChipsAndClearGoesBackToEverything`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| confirmed only leaves the answers given by a bare Enter out of the counts | `internal/server` `TestConfirmedOnlyLeavesTheAnswersGivenByABareEnterOutOfTheCounts`, `internal/reports:TestAnswersByDefaultColourNothing` |
| the matrix takes every run filter, with the count of runs behind it | `internal/server` `TestTheMatrixTakesEveryRunFilterWithTheCountOfRunsBehindIt`, `internal/reports:TestTheMatrixsResultFilters` |
| the matrix's layer filter keeps one layer's columns | `internal/server` `TestTheMatrixTakesEveryRunFilterWithTheCountOfRunsBehindIt`, `internal/reports:TestTheMatrixsResultFilters` |
| the matrix's state filter keeps the rows and columns with a cell in that state | `internal/server` `TestTheMatrixTakesEveryRunFilterWithTheCountOfRunsBehindIt`, `internal/reports:TestTheMatrixsResultFilters` |
| confirmed only hides the matrix cells community machines don't agree on | `internal/server` `TestTheMatrixTakesEveryRunFilterWithTheCountOfRunsBehindIt`, `internal/reports:TestTheMatrixsResultFilters` |
| the old matrix and reports links keep working, and keep the other filters | `internal/server` `TestTheOldMatrixLinksKeepWorkingAndKeepTheOtherFilters`, `internal/reports:TestEveryRunFilterNarrowsAndTheyCombine` |
| the reports exports take the filters, and without any are unchanged | `internal/server` `TestTheReportsExportsTakeTheFilters`, `TestReportsJSONHasEveryReportAsUploadedUnderCC0` |
| checks.csv takes the run filters and the result filters | `internal/server` `TestChecksCSVTakesTheRunFiltersAndTheResultFilters` |
| matrix.json takes the filters and ?group=release, and without any is unchanged | `internal/server` `TestMatrixJSONTakesTheFiltersAndTheGrouping`, `TestMatrixJSONHasEachCellsStateAndPerMachineAgreement` |
| the pages link the exports with the same filters | `internal/server` `TestFiltersCombineShowAsRemovableChipsAndClearGoesBackToEverything`, `TestTheOldMatrixLinksKeepWorkingAndKeepTheOtherFilters` |

## gaps_inventory_test.rb

`site/test/integration/gaps_inventory_test.rb`, 2 cases.

| Case | Held by |
|---|---|
| the kernel-gap page lists hardware no driver claims, unknown hardware first | `internal/server` `TestTheKernelGapPageListsHardwareNoDriverClaimsUnknownHardwareFirst` |
| reports without an inventory add nothing | `internal/server` `TestTheKernelGapPageListsHardwareNoDriverClaimsUnknownHardwareFirst` |

## issue_prefill_test.rb

`site/test/integration/issue_prefill_test.rb`, 5 cases.

| Case | Held by |
|---|---|
| visitors see no issue links | `internal/server` `TestOnlyAMaintainerSeesIssueLinks` |
| a failing check's issue goes to its layer's project, with the report, the Mac and the evidence | `internal/server` `TestAFailingChecksIssueGoesToItsLayersProjectWithTheReportTheMacAndTheEvidence` |
| a regression's issue says so and links the last verified pass | `internal/server` `TestARegressionsIssueSaysSoAndLinksTheLastVerifiedPass` |
| every gap on the kernel-gap page has one | `internal/server` `TestEveryGapOnTheKernelGapPageHasAnIssue` |
| hidden reports get no issue, and long evidence is cut short with its fence closed | `internal/server` `TestLongEvidenceIsCutShortWithItsFenceClosed`, `TestHiddenReportsAndWhoUploadedEachShowToMaintainersOnly`, `internal/reports:TestAnIssuesBodyIsCutToFitALinkWithItsCodeFenceClosed` |

## mac_lineup_test.rb

`site/test/integration/mac_lineup_test.rb`, 8 cases.

| Case | Held by |
|---|---|
| untested Macs show and ask for testing | `internal/server` `TestTheBringUpTableShowsEveryMacTestedOrNot`, `internal/reports:TestEveryMacOfTheCatalogueShowsWithWhatTheCatalogueExpects` |
| every Mac in the catalogue shows, M4 and M5 included | `internal/server` `TestTheBringUpTableShowsEveryMacTestedOrNot`, `internal/reports:TestEveryMacOfTheCatalogueShowsWithWhatTheCatalogueExpects` |
| an untested Mac shows what the catalogue expects | `internal/server` `TestTheBringUpTableShowsEveryMacTestedOrNot`, `internal/reports:TestEveryMacOfTheCatalogueShowsWithWhatTheCatalogueExpects` |
| the runs decide once a Mac is tested, over the catalogue | `internal/server` `TestTheRunsDecideOnceAMacIsTestedOverTheCatalogue`, `internal/reports:TestTheRunsDecideOnceAMacIsTestedOverTheCatalogue` |
| a reinstalled tester Mac's newer run replaces its older one | `internal/reports` `TestAReinstalledMacsNewerVerifiedRunReplacesItsOlderOne` |
| a newer pass retires older failures on its own board, not on another | `internal/server` `TestTheRunsDecideOnceAMacIsTestedOverTheCatalogue`, `internal/reports:TestANewerPassRetiresOlderFailuresOnItsOwnBoardNotOnAnother` |
| one community machine is unconfirmed; two that agree confirm it | `internal/server` `TestTheBringUpTableShowsEveryMacTestedOrNot`, `internal/reports:TestOneCommunityMachineIsUnconfirmedTwoThatAgreeConfirmIt` |
| a tested board shows its runs and what they found | `internal/server` `TestTheBringUpTableShowsEveryMacTestedOrNot` |

## machine_signature_test.rb

`site/test/integration/machine_signature_test.rb`, 9 cases.

| Case | Held by |
|---|---|
| the reports the CLI signed are accepted as they are, whatever characters they hold | `internal/reports` `TestTheReportsTheCLISignedAreAcceptedAsTheyAre` |
| a report changed after it was signed is refused and not stored | `internal/reports` `TestAReportChangedAfterItWasSignedIsRefused` |
| a signature by another key than the report's public key is refused | `internal/reports` `TestASignatureByAnotherKeyThanTheReportsPublicKeyIsRefused` |
| signatures that aren't SSH report signatures are refused | `internal/reports` `TestSignaturesThatArentSSHReportSignaturesAreRefused` |
| an unsigned report is refused with upgrade instructions | `internal/reports` `TestAnUnsignedReportIsRefusedWithUpgradeInstructions` |
| the machine is a keyed digest of its key; the key and signature aren't kept, shown or exported | `internal/reports` `TestTheMachineIsAKeyedDigestOfItsKeyAndTheKeyAndSignatureArentKept`, `TestTheDeletionSecretTheKeyAndTheSignatureAreNotStored`, `internal/server:TestAReportsOldLinkShowsTheMachineAndEachCheck`, `internal/server:TestReportsJSONHasEveryReportAsUploadedUnderCC0`, `internal/server:TestVerifiedRunsOnlyOrCommunityRunsOnly` |
| reports signed by the same key are one machine, from any network; different keys are different machines | `internal/reports` `TestReportsSignedByTheSameKeyAreOneMachineAndDifferentKeysAreDifferentMachines`, `internal/server:TestASingleCommunityRunIsListedButLeavesTheMatrixUnconfirmed` |
| the signature, which holds the public key, never reaches the log | `internal/server` `TestTheDeletionSecretLabdIssuedIsInNoLog` |
| reports the site refuses anyway don't count towards the machine's limit | `internal/reports` `TestReportsTheLabRefusesAnywayDontCountTowardsTheMachinesLimit` |

## matrix_test.rb

`site/test/integration/matrix_test.rb`, 10 cases.

| Case | Held by |
|---|---|
| a single community report is listed but leaves the matrix unconfirmed | `internal/server` `TestASingleCommunityRunIsListedButLeavesTheMatrixUnconfirmed`, `internal/reports:TestCommunityRunsColourACellOnlyOnceTwoMachinesAgree`, `TestTheReportsPageListsEveryPublishedReportNewestFirst` |
| two reports from the same machine don't confirm each other | `internal/server` `TestASingleCommunityRunIsListedButLeavesTheMatrixUnconfirmed`, `internal/reports:TestCommunityRunsColourACellOnlyOnceTwoMachinesAgree` |
| two distinct machines agreeing colour the cell | `internal/server` `TestASingleCommunityRunIsListedButLeavesTheMatrixUnconfirmed`, `internal/reports:TestCommunityRunsColourACellOnlyOnceTwoMachinesAgree` |
| agreeing failures are orange, missing support blue and unknown hardware magenta | `internal/server` `TestAgreeingFailuresAreOrangeMissingSupportBlueAndUnknownHardwareMagenta`, `internal/reports:TestAgreeingFailuresMissingSupportAndUnknownHardwareEachHaveTheirState` |
| machines that disagree leave the cell unconfirmed, until each side has two machines: partial | `internal/reports` `TestMachinesThatDisagreeLeaveTheCellUnconfirmedUntilEachSideHasTwo` |
| each machine counts with the latest state it tested | `internal/reports` `TestEachMachineCountsWithTheLatestStateItTested` |
| features no machine tested are grey | `internal/reports` `TestFeaturesNoMachineTestedAreNotTested` |
| rows are per model and stack/version, and the stack filter narrows them | `internal/server` `TestRowsArePerModelAndStackVersionAndTheStackFilterNarrowsThem`, `internal/reports:TestRowsArePerModelAndStackVersionNewestVersionFirst` |
| the home page shows the matrix, or says there are no reports yet | `internal/server` `TestTheHomePageShowsTheMatrixOrSaysThereAreNoReportsYet` |
| reports uploaded before machine ids existed count as one machine | `internal/reports` `TestReportsFromBeforeMachineKeysCountAsOneMachine`, `internal/server:TestASingleCommunityRunIsListedButLeavesTheMatrixUnconfirmed` |

## model_results_test.rb

`site/test/integration/model_results_test.rb`, 9 cases.

| Case | Held by |
|---|---|
| the default model view replaces a failure with a later pass and preserves it through a skip | `internal/server` `TestTheDefaultModelViewReplacesAFailureWithALaterPassAndPreservesItThroughASkip`, `internal/reports:TestTheModelSummaryReplacesAFailureWithALaterPassAndKeepsItThroughASkip` |
| a later failure on another release supersedes the pass and a skip keeps the regression | `internal/server` `TestALaterFailureSupersedesThePassAndUploadOrderDecides`, `internal/reports:TestALaterFailureOnAnotherReleaseSupersedesThePassAndASkipKeepsTheRegression` |
| upload order decides the latest result even when a machine retests an older kernel | `internal/server` `TestALaterFailureSupersedesThePassAndUploadOrderDecides`, `internal/reports:TestUploadOrderDecidesTheLatestResultEvenWhenAMachineRetestsAnOlderKernel` |
| a carried result identifies its actual source kernel and date | `internal/server` `TestACarriedResultIdentifiesItsActualSourceKernelAndDate`, `internal/reports:TestACarriedResultIdentifiesItsActualSourceKernelAndDate` |
| skipping one check retains its previous result when another check of the feature passes | `internal/server` `TestSkippedChecksConfirmedSummariesAndWhoMayClaimAResult`, `internal/reports:TestSkippingOneCheckRetainsItsPreviousResultWhenAnotherCheckOfTheFeaturePasses` |
| confirmed summaries still show the unconfirmed runs that established agreement | `internal/server` `TestSkippedChecksConfirmedSummariesAndWhoMayClaimAResult`, `internal/reports:TestConfirmedSummariesStillShowTheUnconfirmedRunsThatEstablishedAgreement` |
| a newer community result cannot claim the source of a tester-confirmed result | `internal/server` `TestSkippedChecksConfirmedSummariesAndWhoMayClaimAResult`, `internal/reports:TestANewerCommunityResultCannotClaimTheSourceOfAVerifiedResult` |
| different machines still disagree and different stacks have separate summaries | `internal/server` `TestSkippedChecksConfirmedSummariesAndWhoMayClaimAResult`, `internal/reports:TestDifferentMachinesStillDisagreeAndDifferentStacksHaveSeparateSummaries` |
| the model export contains the same results, filters and source metadata | `internal/server` `TestMatrixJSONTakesTheFiltersAndTheGrouping`, `internal/reports:TestTheCountsAreTheOldSites` |

## pages_test.rb

`site/test/integration/pages_test.rb`, 11 cases.

| Case | Held by |
|---|---|
| the models page lists each Mac with its stacks and report count | `internal/server` `TestTheModelsPageListsEachMacWithItsStacksAndReportCount` |
| a model page shows what its chip is expected to do and each stack's results | `internal/server` `TestAModelPageShowsWhatItsChipIsExpectedToDoAndEachStacksResults` |
| a model without visible reports is not found | `internal/server` `TestAModelPageShowsWhatItsChipIsExpectedToDoAndEachStacksResults` |
| the features page keeps compact results with accessible model and build details | `internal/server` `TestTheFeaturesPageKeepsCompactResultsWithAccessibleModelAndBuildDetails` |
| a feature page shows per-chip expectations and results per Mac | `internal/server` `TestAFeaturePageShowsPerChipExpectationsAndResultsPerMac` |
| a feature no check covers says so | `internal/server` `TestAFeaturePageShowsPerChipExpectationsAndResultsPerMac` |
| Touch ID lists its readiness and human unlock checks | `internal/server` `TestAFeaturePageShowsPerChipExpectationsAndResultsPerMac` |
| an unknown feature is not found | `internal/server` `TestAFeaturePageShowsPerChipExpectationsAndResultsPerMac` |
| the kernel-gap list shows reported gaps and what Asahi has but Aurora hasn't verified | `internal/server` `TestTheKernelGapListShowsReportedGapsAndWhatAsahiHasButAuroraHasNotVerified` |
| the reports page lists every visible report, newest first | `internal/server` `TestTheReportsPageListsEveryPublishedReportNewestFirst` |
| the report page reads like terminal output, grouped by section | `internal/server` `TestTheReportPageReadsLikeTerminalOutputWithItsBuildAndBadge` |

## regressions_test.rb

`site/test/integration/regressions_test.rb`, 7 cases.

| Case | Held by |
|---|---|
| a failure with no verified earlier pass is not a regression | `internal/server` `TestARegressionOnAReportLinksTheVerifiedPass`, `internal/reports:TestAFailureIsARegressionOnlyAfterAVerifiedPassOnTheSameModelAndStack` |
| a failure after a tester's pass on the same model and stack is a regression, linked to that pass | `internal/server` `TestARegressionOnAReportLinksTheVerifiedPass`, `internal/reports:TestAFailureIsARegressionOnlyAfterAVerifiedPassOnTheSameModelAndStack` |
| a community pass, a hidden tester pass or a removed tester's pass doesn't make it a regression | `internal/server` `TestARegressionOnAReportLinksTheVerifiedPass`, `internal/reports:TestAFailureIsARegressionOnlyAfterAVerifiedPassOnTheSameModelAndStack` |
| only the same model and stack, and only an earlier pass, count | `internal/server` `TestOnlyTheSameModelAndStackCountAndARegressionStaysOpenUntilItWorksAgain`, `TestARegressionOnAReportLinksTheVerifiedPass`, `internal/reports:TestAFailureIsARegressionOnlyAfterAVerifiedPassOnTheSameModelAndStack` |
| a regression stays open until a newer run on that model and stack works again | `internal/server` `TestOnlyTheSameModelAndStackCountAndARegressionStaysOpenUntilItWorksAgain`, `internal/reports:TestARegressionStaysOpenUntilANewerRunOnThatModelAndStackWorksAgain` |
| two machines agreeing on a regression colour the cell red; the legend tells regressions from failures | `internal/server` `TestTwoMachinesAgreeingOnARegressionColourTheCellRed`, `internal/reports:TestTwoMachinesAgreeingOnARegressionColourTheCell` |
| a tester's regression on a candidate set is named in the verdict | `internal/server` `TestCandidatesSaysPassingFailingOrWaiting` |

## report_evidence_test.rb

`site/test/integration/report_evidence_test.rb`, 4 cases.

| Case | Held by |
|---|---|
| every golden report is within the evidence rules | `internal/reports` `TestEveryGoldenReportIsWithinTheRules` |
| a report with more than 64 KiB of evidence is refused | `internal/reports` `TestAReportWithMoreThan64KiBOfEvidenceIsRefused` |
| 64 KiB of evidence is accepted | `internal/reports` `TestSixtyFourKiBOfEvidenceIsAccepted` |
| evidence that isn't text is refused | `internal/reports` `TestEvidenceThatIsntTextIsRefused` |

## report_inventory_test.rb

`site/test/integration/report_inventory_test.rb`, 5 cases.

| Case | Held by |
|---|---|
| the report page lists the hardware no driver claimed, unknown hardware first | `internal/server` `TestTheReportPageListsTheHardwareNoDriverClaimed` |
| the report page lists the kernel's build options that differ from Asahi's | `internal/server` `TestTheReportPageListsTheHardwareNoDriverClaimed` |
| the M1 has no unknown hardware: its DisplayPort audio isn't in Asahi yet | `internal/server` `TestTheReportPageListsTheHardwareNoDriverClaimed` |
| a report without an inventory shows no hardware section | `internal/server` `TestTheReportPageListsTheHardwareNoDriverClaimed` |
| inventory fields outside the schema are rejected | `internal/reports` `TestInventoryFieldsOutsideTheSchemaAreRejected`, `TestVerdictsAreTheOldSites` |

## report_upload_test.rb

`site/test/integration/report_upload_test.rb`, 13 cases.

| Case | Held by |
|---|---|
| posting the golden report stores it and returns report and deletion links | `internal/reports` `TestPostingTheGoldenReportStoresItAndReturnsReportAndDeletionLinks`, `internal/server:TestAnUploadIsAnsweredWithLinksAtTheLabAddress` |
| the report page shows the machine and each check | `internal/server` `TestAReportsOldLinkShowsTheMachineAndEachCheck` |
| every golden report is accepted | `internal/reports` `TestEveryGoldenReportIsAccepted` |
| reports that don't match the schema are rejected and not stored | `internal/reports` `TestReportsThatDontMatchTheSchemaAreRejectedAndNotStored` |
| check ids the feature catalogue doesn't know are rejected with an upgrade message | `internal/reports` `TestCheckIDsTheFeatureCatalogueDoesntKnowAreRejectedWithAnUpgradeMessage`, `TestARefusalIsAnsweredAsTheOldSiteAnsweredIt` |
| every check id in the golden reports is in the feature catalogue | `internal/reports` `TestEveryGoldenReportIsWithinTheRules` |
| the report page explains each result in plain words, with its layer and catalogue version | `internal/server` `TestAReportsOldLinkShowsTheMachineAndEachCheck`, `TestTheReportPageExplainsEachResultInPlainWords` |
| a failed result shows the catalogue's words for its outcome | `internal/server` `TestTheReportPageExplainsEachResultInPlainWords`, `internal/reports:TestTheCatalogue` |
| a body that isn't JSON is a bad request | `internal/reports` `TestABodyThatIsntJSONIsABadRequestAndNotStored`, `TestABodyThatIsntJSONIsABadRequest` |
| oversized reports are refused before parsing | `internal/reports` `TestOversizedReportsAreRefusedBeforeParsing` |
| the deletion link deletes the report | `internal/server` `TestTheDeletionLinkAsksBeforeDeleting`, `internal/reports:TestADeletionLinkDeletesItsReportAndNoOther` |
| deleting removes the report and its evidence completely, hidden or not | `internal/reports` `TestDeletingRemovesTheReportAndItsEvidenceCompletelyHiddenOrNot`, `internal/server:TestTheDeletionLinkDeletesAHiddenReport`, `internal/server:TestHiddenAndDeletedReportsAreLeftOutOfEveryExport` |
| a wrong deletion token deletes nothing | `internal/server` `TestTheDeletionLinkAsksBeforeDeleting`, `internal/reports:TestADeletionLinkDeletesItsReportAndNoOther` |

## site_test.rb

`site/test/integration/site_test.rb`, 5 cases.

| Case | Held by |
|---|---|
| the installer is served as a shell script, byte for byte | `internal/server` `TestTheInstallerIsServedAsAShellScriptByteForByteAndPublicForFiveMinutes` |
| the installer pins the same release key as the release workflow and README | `internal/repotest` `TestReleaseKeyPins`, `internal/server:TestTheInstallLinePinsTheReleaseKeyItsLabWasBuiltWith` |
| the home page shows the install command | `internal/server` `TestTheHomePageShowsTheMatrixOrSaysThereAreNoReportsYet` |
| www redirects to the apex keeping path and query | Dropped. The lab answers to the Lab address and the Alias alone; the old names are forwarded by an edge rule at the opening (spec 6 "The Alias", 16 step 25). |
| the apex itself is not redirected | `internal/server` `TestTheInstallerIsServedAsAShellScriptByteForByteAndPublicForFiveMinutes` |

## tester_gating_test.rb

`site/test/integration/tester_gating_test.rb`, 13 cases.

| Case | Held by |
|---|---|
| one tester run colours the cell on its own, framed as tester-verified | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns` |
| a machine bound to a handle off the allowlist is still one community machine | `internal/reports` `TestAMachinesLatestSignInDecidesEachRunAsItIsUploaded`, `internal/server:TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns` |
| tester runs outweigh community runs; a newer tester pass retires an older tester failure | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns` |
| a newer tester failure stands: an older pass doesn't retire it, so testers disagree | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns` |
| a machine that fails after it passed can't retire another machine's failure | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns` |
| a machine's latest run decides: a community run after its tester run takes the tester colour away | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns`, `internal/reports:TestAMachinesLatestSignInDecidesEachRunAsItIsUploaded` |
| removing a tester from the allowlist takes their runs out of the matrix colours | Dropped. The verdict is stamped on a report at upload and never recomputed, so a person who leaves the M team keeps their earlier Verified runs (spec 3 "Kinds of people", 5 "Verified runs"); the new rule is held by TestRemovingTheMarkLaterChangesNoStoredReport. |
| matrix.json counts tester machines per cell | `internal/server` `TestVerifiedRunsColourTheMatrixOnTheirOwnAndOutweighCommunityRuns`, `TestMatrixJSONHasEachCellsStateAndPerMachineAgreement` |
| a candidate set with only community runs waits for testers | `internal/server` `TestACandidateSetsPageShowsWhatVerifiedRunsFoundPerModelAndFeature`, `TestCandidatesSaysPassingFailingOrWaiting` |
| tester runs on every feature working make the set ready; the view is per model and feature, tester runs only | `internal/server` `TestACandidateSetsPageShowsWhatVerifiedRunsFoundPerModelAndFeature`, `TestCandidatesSaysPassingFailingOrWaiting` |
| a tester's failure blocks the set and is listed | `internal/server` `TestACandidateSetsPageShowsWhatVerifiedRunsFoundPerModelAndFeature` |
| hidden reports don't count, and an unknown set is not found | `internal/server` `TestACandidateSetsPageShowsWhatVerifiedRunsFoundPerModelAndFeature`, `TestTheCandidatesPageMapsEachBuildToItsCandidateSetWhenItIsKnown` |
| the report page links its candidate set | `internal/server` `TestACandidateSetsPageShowsWhatVerifiedRunsFoundPerModelAndFeature` |

## tester_requests_test.rb

`site/test/integration/tester_requests_test.rb`, 11 cases.

| Case | Held by |
|---|---|
| status: a machine that never signed in isn't signed in | `internal/reports` `TestStatusAMachineThatNeverSignedInIsNotSignedIn` |
| status: after the golden sign-in, the machine is signed in as its handle, a tester while it's on the allowlist | `internal/reports` `TestStatusAfterTheGoldenSignInTheMachineIsSignedInAsItsHandleVerifiedWhileOnTheMTeam` |
| sign-out unbinds the machine: its later runs are community runs, earlier ones keep their handle | `internal/reports` `TestSignOutUnbindsTheMachineItsLaterRunsAreCommunityRunsEarlierOnesKeepTheirHandle` |
| sign-out of a machine that isn't bound says so and changes nothing else | `internal/reports` `TestSignOutOfAMachineThatIsNotBoundSaysSoAndChangesNothingElse` |
| a machine only ever signs itself out | `internal/reports` `TestAMachineOnlyEverSignsItselfOut` |
| a request dated more than a day from the site's clock is refused | `internal/reports` `TestARequestDatedMoreThanADayFromTheLabsClockIsRefused` |
| a copy of a sign-out, replayed after the machine signed in again, unbinds nothing | `internal/reports` `TestACopyOfASignOutReplayedAfterTheMachineSignedInAgainUnbindsNothing` |
| a copy of a sign-out, after the same handle signed in again on the machine, unbinds nothing | `internal/reports` `TestACopyOfASignOutAfterTheSameHandleSignedInAgainOnTheMachineUnbindsNothing` |
| a sign-in racing a sign-out is never lost | `internal/reports` `TestASignInRacingASignOutIsNeverLost` |
| a request changed after it was signed, or signed as a report, is refused | `internal/reports` `TestARequestChangedAfterItWasSignedOrSignedAsAReportIsRefused` |
| a sign-in can't pass for a request, nor anything else | `internal/reports` `TestASignInCannotPassForARequestNorAnythingElse` |

## tester_sign_in_test.rb

`site/test/integration/tester_sign_in_test.rb`, 11 cases.

| Case | Held by |
|---|---|
| the golden sign-in binds the GitHub handle to the machine key, and the token is revoked, never kept | `internal/reports` `TestTheGoldenSignInBindsTheGitHubHandleToTheMachineKeyAndTheTokenIsRevokedNeverKept`, `internal/server:TestMacSignInKeepsTheOldSitesWireNames` |
| after signing in, the machine's runs are tester runs: the upload says so and the report has the tester badge | `internal/reports` `TestAfterSigningInTheMachinesRunsAreVerifiedRunsAndTheUploadSaysSo`, `internal/server:TestARunIsVerifiedOnlyWhenItsMacIsSignedInAsAnMTeamMemberAtUpload`, `internal/server:TestVerifiedRunsOnlyOrCommunityRunsOnly` |
| a handle that isn't on the allowlist is bound, but its runs stay community runs until the admin adds it | `internal/reports` `TestASignInByAPersonOutsideTheMTeamStaysAndItsRunsAreCommunityRuns`, `internal/server:TestASignInFromOutsideTheMTeamStaysAndPeopleShowsItNotCounted` |
| a token another app issued, or an unknown one, binds nothing | `internal/reports` `TestATokenAnotherAppIssuedOrAnUnknownOneBindsNothing`, `internal/server:TestMacSignInKeepsTheOldSitesWireNames` |
| a sign-in changed after it was signed, or signed as a report, is refused | `internal/reports` `TestASignInChangedAfterItWasSignedOrSignedAsAReportIsRefused` |
| anything but a sign-in is refused | `internal/reports` `TestAnythingButASignInIsRefused` |
| without the GitHub app's secret there is no tester sign-in | `internal/reports` `TestWithoutTheGitHubAppThereIsNoMacSignIn`, `internal/server:TestALabThatCannotSignAMacInSaysSoItself` |
| signing in again on the machine rebinds it; earlier runs keep the handle they were uploaded under | `internal/reports` `TestSigningInAgainOnTheMachineRebindsItAndEarlierRunsKeepTheHandleTheyWereUploadedUnder` |
| an allowlisted handle is pinned to the first GitHub account that signs in with it | `internal/reports` `TestAnotherAccountUnderAMembersHandleIsNotThatMember`, `internal/server:TestMarkingAHandleLooksItUpOnGitHubAndPinsTheAccount` |
| an oversized sign-in is refused, also when sent chunked | `internal/reports` `TestAnOversizedSignInIsRefusedAlsoWhenSentChunked` |
| sign-ins are rate-limited per network | `internal/reports` `TestSignInsAreLimitedToTenAnHourPerAddress`, `internal/server:TestMacSignInIsLimitedOnTheCallersAddress` |

## unattended_run_test.rb

`site/test/integration/unattended_run_test.rb`, 3 cases.

| Case | Held by |
|---|---|
| an unattended report is accepted and its page says nobody was at the Mac | `internal/reports` `TestAnUnattendedReportIsAcceptedAndUnattendedCanOnlyBeTrue`, `internal/server:TestTheReportPageExplainsEachResultInPlainWords` |
| an attended report doesn't say unattended | `internal/server` `TestAReportsOldLinkShowsTheMachineAndEachCheck` |
| unattended can only be true | `internal/reports` `TestAnUnattendedReportIsAcceptedAndUnattendedCanOnlyBeTrue` |

## upload_rules_test.rb

`site/test/integration/upload_rules_test.rb`, 6 cases.

| Case | Held by |
|---|---|
| a report in an outdated schema is refused with an upgrade message | `internal/reports` `TestAReportInAnOutdatedSchemaIsRefusedWithTheLabsInstallLine`, `TestAReportInAnOutdatedSchemaIsRefusedWithAnUpgradeMessage`, `internal/server:TestAnUploadIsAnsweredWithLinksAtTheLabAddress` |
| unknown check ids get the same upgrade instructions | `internal/reports` `TestCheckIDsTheFeatureCatalogueDoesntKnowAreRejectedWithAnUpgradeMessage`, `TestARefusalIsAnsweredAsTheOldSiteAnsweredIt` |
| uploads are rate-limited per IP address, whichever machines they come from | `internal/reports` `TestUploadsAreRateLimitedPerAddressWhicheverMachinesTheyComeFrom`, `TestAFloodOfJunkUsesUpAnAddresssUploads` |
| uploads are rate-limited per machine key, from any network | `internal/reports` `TestUploadsAreRateLimitedPerMachineKeyFromAnyNetwork` |
| reports with a forged signature don't use up the real machine's uploads | `internal/reports` `TestReportsWithAForgedSignatureDontUseUpTheRealMachinesUploads` |
| behind a proxy the client address comes from the configured header, never from X-Forwarded-For | `internal/server` `TestUploadsAreCountedOnCFConnectingIP` |

## matrix_cell_test.rb

`site/test/models/matrix_cell_test.rb`, 2 cases.

| Case | Held by |
|---|---|
| a skip on hardware the Mac doesn't have counts as not on this Mac | `internal/reports` `TestWhichSkippedChecksStillCountInAModelSummary` |
| any other skip leaves the cell untested | `internal/reports` `TestWhichSkippedChecksStillCountInAModelSummary` |

## omarchy_release_test.rb

`site/test/models/omarchy_release_test.rb`, 2 cases.

| Case | Held by |
|---|---|
| a package version names its release and whether it is the release itself | `internal/reports` `TestAPackageVersionNamesItsReleaseAndWhetherItIsTheReleaseItself` |
| pre-releases sort below their release | `internal/reports` `TestPreReleasesSortBelowTheirRelease` |
